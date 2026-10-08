package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"icloud-hme/internal/account"
	"icloud-hme/internal/hme"
	"icloud-hme/internal/mail"
)

type fakeSharingBackend struct {
	fakeBackend
	mu                              sync.Mutex
	accountAliases                  map[string][]hme.Alias
	boundary                        mail.ShareBoundary
	queryAccount, queryEmail, query string
	queryBoundary                   mail.ShareBoundary
	started                         chan struct{}
	release                         chan struct{}
	offline                         bool
}

func sharingFake() *fakeSharingBackend {
	return &fakeSharingBackend{
		fakeBackend: fakeBackend{accounts: []account.Summary{{ID: "a"}, {ID: "b"}}},
		accountAliases: map[string][]hme.Alias{
			"a": {{AnonymousID: "one", Email: "one@icloud.com"}, {AnonymousID: "two", Email: "two@icloud.com"}},
			"b": {{AnonymousID: "three", Email: "three@icloud.com"}},
		},
		boundary: mail.ShareBoundary{MailboxIdentity: "test-mailbox", ConfigurationVersion: "v1", MinUID: 100, UIDValidity: 7},
	}
}

func (f *fakeSharingBackend) ListAccounts() []account.Summary {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]account.Summary(nil), f.accounts...)
}
func (f *fakeSharingBackend) ListAliases(id string) ([]hme.Alias, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.offline {
		return nil, fmt.Errorf("upstream unavailable")
	}
	return append([]hme.Alias(nil), f.accountAliases[id]...), nil
}
func (f *fakeSharingBackend) CaptureShareBoundary(string) (mail.ShareBoundary, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.boundary, nil
}
func (f *fakeSharingBackend) ValidateShareBoundary(id string, boundary mail.ShareBoundary) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.offline {
		return fmt.Errorf("mailbox unavailable")
	}
	if boundary.MailboxIdentity != f.boundary.MailboxIdentity || boundary.ConfigurationVersion != f.boundary.ConfigurationVersion || boundary.UIDValidity != f.boundary.UIDValidity {
		return shareUnavailableError()
	}
	return nil
}

func (f *fakeSharingBackend) ValidateShareConfiguration(id string, boundary mail.ShareBoundary) error {
	return f.ValidateShareBoundary(id, boundary)
}
func (f *fakeSharingBackend) WithShareConfiguration(id string, boundary mail.ShareBoundary, commit func() error) error {
	if err := f.ValidateShareConfiguration(id, boundary); err != nil {
		return err
	}
	if commit != nil {
		return commit()
	}
	return nil
}
func (f *fakeSharingBackend) SearchShared(id string, boundary mail.ShareBoundary, email string, page, pageSize int, query string) (mail.SearchResult, error) {
	f.mu.Lock()
	f.queryAccount, f.queryEmail, f.query, f.queryBoundary = id, email, query, boundary
	started, release := f.started, f.release
	f.mu.Unlock()
	if started != nil {
		started <- struct{}{}
		<-release
	}
	return mail.SearchResult{Messages: []mail.Message{{ID: "100", To: email, Subject: "after creation"}}, Total: 1, Page: page, PageSize: pageSize}, nil
}
func (f *fakeSharingBackend) GetSharedFull(id string, boundary mail.ShareBoundary, email string, uid uint32) (*mail.FullMessage, error) {
	if uid < boundary.MinUID || uid > 101 {
		return nil, messageNotFoundError()
	}
	return &mail.FullMessage{Message: mail.Message{ID: fmt.Sprint(uid), To: email}, Body: "customer message"}, nil
}

func newSharingTest(t *testing.T, f *fakeSharingBackend, dir string) (*Server, *httptest.Server, string, string) {
	t.Helper()
	s := newWithBackend(f, Config{AdminPassword: "admin-pass-2026-strong", DataDir: dir})
	var err error
	s.shares, err = newShareStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	cookie, csrf := login(t, ts, "admin-pass-2026-strong")
	return s, ts, cookie, csrf
}

func shareAdminRequest(t *testing.T, ts *httptest.Server, method, path, cookie, csrf, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: "hme_session", Value: cookie})
	}
	req.Header.Set("X-CSRF-Token", csrf)
	req.Header.Set("Content-Type", "application/json")
	status, data, _ := do(t, req)
	return status, data
}

func createTestShare(t *testing.T, ts *httptest.Server, cookie, csrf, accountID, aliasID string) string {
	t.Helper()
	status, body := shareAdminRequest(t, ts, "POST", "/api/aliases/"+aliasID+"/share", cookie, csrf, `{"account_id":"`+accountID+`"}`)
	if status != 200 {
		t.Fatalf("create share status=%d: %s", status, body)
	}
	var out struct {
		Data struct {
			Token     string
			Active    bool
			CreatedAt string `json:"created_at"`
		}
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Data.Token) != 43 || !out.Data.Active {
		t.Fatal("missing random capability")
	}
	if _, err := time.Parse(time.RFC3339, out.Data.CreatedAt); err != nil {
		t.Fatal(err)
	}
	return out.Data.Token
}

func sharedRequest(t *testing.T, ts *httptest.Server, method, path, token string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(method, ts.URL+path, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.Header.Get("Cache-Control") != "no-store" || response.Header.Get("Referrer-Policy") != "no-referrer" {
		t.Fatal("missing API security headers")
	}
	if len(response.Cookies()) != 0 {
		t.Fatal("public request issued a session")
	}
	var data strings.Builder
	_, _ = io.Copy(&data, response.Body)
	return response.StatusCode, data.String()
}

func TestShareAdminAuthenticationAndCSRF(t *testing.T) {
	_, ts, cookie, csrf := newSharingTest(t, sharingFake(), "")
	for _, method := range []string{"GET", "POST", "DELETE"} {
		path := "/api/aliases/one/share"
		if method == "GET" {
			path += "?account_id=a"
		}
		status, _ := shareAdminRequest(t, ts, method, path, "", "", `{"account_id":"a"}`)
		if status != 401 {
			t.Errorf("%s without admin=%d", method, status)
		}
		if method != "GET" {
			status, _ = shareAdminRequest(t, ts, method, path, cookie, "", `{"account_id":"a"}`)
			if status != 403 {
				t.Errorf("%s without CSRF=%d", method, status)
			}
		}
	}
	status, body := shareAdminRequest(t, ts, "GET", "/api/aliases/one/share?account_id=a", cookie, csrf, "")
	if status != 200 || !strings.Contains(body, `"active":false`) {
		t.Fatal(body)
	}
	token := createTestShare(t, ts, cookie, csrf, "a", "one")
	status, body = shareAdminRequest(t, ts, "GET", "/api/aliases/one/share?account_id=a", cookie, csrf, "")
	if status != 200 || !strings.Contains(body, `"active":true`) || strings.Contains(body, token) || strings.Contains(body, `"token"`) {
		t.Fatalf("unsafe status response: %s", body)
	}
	status, body = sharedRequest(t, ts, "GET", "/api/accounts", token)
	if status != 401 || !strings.Contains(body, "AUTH_REQUIRED") {
		t.Fatal("share granted administrative access")
	}
	status, _ = shareAdminRequest(t, ts, "POST", "/api/aliases/three/share", cookie, csrf, `{"account_id":"a"}`)
	if status != 404 {
		t.Fatal("alias from another account was shared")
	}
}

func TestShareAdminCanInspectAndRevokeWhileUpstreamOffline(t *testing.T) {
	f := sharingFake()
	_, ts, cookie, csrf := newSharingTest(t, f, "")
	token := createTestShare(t, ts, cookie, csrf, "a", "one")
	f.mu.Lock()
	f.offline = true
	f.mu.Unlock()
	status, body := shareAdminRequest(t, ts, "GET", "/api/aliases/one/share?account_id=a", cookie, csrf, "")
	if status != 200 || !strings.Contains(body, `"active":true`) {
		t.Fatalf("upstream failure hid revoke control: %d %s", status, body)
	}
	status, body = shareAdminRequest(t, ts, "DELETE", "/api/aliases/one/share", cookie, csrf, `{"account_id":"a"}`)
	if status != 200 || !strings.Contains(body, `"active":false`) {
		t.Fatalf("offline revoke failed: %d %s", status, body)
	}
	if status, _ := sharedRequest(t, ts, "GET", "/api/shared", token); status != 404 {
		t.Fatal("offline revoke retained capability")
	}
}

func TestShareTemporaryAliasFailurePreservesTokenAcrossRecoveryAndRestart(t *testing.T) {
	dir := t.TempDir()
	f := sharingFake()
	_, ts, cookie, csrf := newSharingTest(t, f, dir)
	token := createTestShare(t, ts, cookie, csrf, "a", "one")
	f.mu.Lock()
	f.offline = true
	f.mu.Unlock()
	for _, path := range []string{"/api/shared", "/api/shared/inbox", "/api/shared/inbox/100"} {
		status, body := sharedRequest(t, ts, "GET", path, token)
		if status != 503 || !strings.Contains(body, "UPSTREAM_FAILURE") || strings.Contains(body, "one@icloud.com") {
			t.Errorf("temporary verification failure: %d %s", status, body)
		}
	}
	f.mu.Lock()
	f.offline = false
	f.mu.Unlock()
	for _, path := range []string{"/api/shared", "/api/shared/inbox", "/api/shared/inbox/100"} {
		if status, body := sharedRequest(t, ts, "GET", path, token); status != 200 {
			t.Errorf("recovered token: %d %s", status, body)
		}
	}
	_, restarted, _, _ := newSharingTest(t, f, dir)
	if status, body := sharedRequest(t, restarted, "GET", "/api/shared", token); status != 200 {
		t.Errorf("restarted token: %d %s", status, body)
	}
}

// deadlineShareWriter models a client that stops reading. Write only returns
// when its deadline expires or the test explicitly releases it.
type deadlineShareWriter struct {
	*httptest.ResponseRecorder
	started chan struct{}
	release chan struct{}
	expired chan struct{}
	once    sync.Once
	timer   *time.Timer
}

func (w *deadlineShareWriter) SetWriteDeadline(deadline time.Time) error {
	if w.timer != nil {
		w.timer.Stop()
	}
	if !deadline.IsZero() {
		w.timer = time.AfterFunc(time.Until(deadline), func() { close(w.expired) })
	}
	return nil
}

func (w *deadlineShareWriter) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.started) })
	select {
	case <-w.release:
		return w.ResponseRecorder.Write(p)
	case <-w.expired:
		return 0, &net.OpError{Op: "write", Err: os.ErrDeadlineExceeded}
	}
}

func TestShareSlowWriterDoesNotBlockOtherGrantsOrRevokeForever(t *testing.T) {
	for _, test := range []struct {
		name, path string
		admin      bool
	}{
		{"metadata", "/api/shared", false},
		{"inbox", "/api/shared/inbox", false},
		{"message", "/api/shared/inbox/100", false},
		{"rotation", "/api/aliases/one/share", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			f := sharingFake()
			s, ts, cookie, csrf := newSharingTest(t, f, "")
			s.shareWriteTimeout = 300 * time.Millisecond
			one := createTestShare(t, ts, cookie, csrf, "a", "one")
			two := createTestShare(t, ts, cookie, csrf, "a", "two")
			writer := &deadlineShareWriter{ResponseRecorder: httptest.NewRecorder(), started: make(chan struct{}), release: make(chan struct{}), expired: make(chan struct{})}
			defer close(writer.release)
			req := httptest.NewRequest("GET", test.path, nil)
			if test.admin {
				req = httptest.NewRequest("POST", test.path, strings.NewReader(`{"account_id":"a"}`))
				req.AddCookie(&http.Cookie{Name: "hme_session", Value: cookie})
				req.Header.Set("X-CSRF-Token", csrf)
				req.Header.Set("Content-Type", "application/json")
			} else {
				req.Header.Set("Authorization", "Bearer "+one)
			}
			slowDone := make(chan struct{})
			go func() { s.Handler().ServeHTTP(writer, req); close(slowDone) }()
			select {
			case <-writer.started:
			case <-time.After(time.Second):
				t.Fatal("shared write did not start")
			}
			otherDone := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				r := httptest.NewRecorder()
				req := httptest.NewRequest("GET", "/api/shared", nil)
				req.Header.Set("Authorization", "Bearer "+two)
				s.Handler().ServeHTTP(r, req)
				otherDone <- r
			}()
			select {
			case r := <-otherDone:
				if r.Code != 200 {
					t.Errorf("other grant: %d %s", r.Code, r.Body.String())
				}
			case <-time.After(150 * time.Millisecond):
				t.Error("slow client held the global share lock")
				return
			}
			// An unrelated grant can be revoked while this client is still blocked.
			status, body := shareAdminRequest(t, ts, "DELETE", "/api/aliases/two/share", cookie, csrf, `{"account_id":"a"}`)
			if status != 200 {
				t.Errorf("unrelated revoke: %d %s", status, body)
			}
			sameRevoke := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				r := httptest.NewRecorder()
				req := httptest.NewRequest("DELETE", "/api/aliases/one/share", strings.NewReader(`{"account_id":"a"}`))
				req.AddCookie(&http.Cookie{Name: "hme_session", Value: cookie})
				req.Header.Set("X-CSRF-Token", csrf)
				req.Header.Set("Content-Type", "application/json")
				s.Handler().ServeHTTP(r, req)
				sameRevoke <- r
			}()
			select {
			case <-sameRevoke:
				t.Error("revoke completed before the earlier response write finished")
			case <-time.After(30 * time.Millisecond):
			}
			select {
			case <-slowDone:
			case <-time.After(time.Second):
				t.Error("response write had no effective deadline")
				return
			}
			select {
			case r := <-sameRevoke:
				if r.Code != 200 {
					t.Errorf("bounded revoke: %d %s", r.Code, r.Body.String())
				}
			case <-time.After(time.Second):
				t.Error("same-grant revoke remained blocked after write timeout")
			}
		})
	}
}

type configurationSwitchBackend struct {
	*fakeSharingBackend
	configuration *managerBackend
	afterSnapshot func()
}

func (b *configurationSwitchBackend) ValidateShareConfiguration(id string, boundary mail.ShareBoundary) error {
	err := b.configuration.ValidateShareConfiguration(id, boundary)
	if err == nil && b.afterSnapshot != nil {
		b.afterSnapshot()
	}
	return err
}

func (b *configurationSwitchBackend) WithShareConfiguration(id string, boundary mail.ShareBoundary, commit func() error) error {
	// Deterministically switch configuration after a successful snapshot check
	// and before the protected commit. The real adapter must check again while
	// holding the account guard through the complete response write.
	if err := b.ValidateShareConfiguration(id, boundary); err != nil {
		return err
	}
	return b.configuration.WithShareConfiguration(id, boundary, commit)
}

func TestSharedResponseRejectsConfigurationSwitchAfterSnapshotCheck(t *testing.T) {
	mgr, err := account.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	summary, err := mgr.AddAccountWithInput(account.AddAccountInput{Name: "shared", ICloudEmail: "first@icloud.com"})
	if err != nil {
		t.Fatal(err)
	}
	acc, _ := mgr.GetAccount(summary.ID)
	f := sharingFake()
	f.accounts = []account.Summary{summary}
	f.accountAliases[summary.ID] = f.accountAliases["a"]
	b := &configurationSwitchBackend{fakeSharingBackend: f, configuration: &managerBackend{mgr: mgr}}
	b.afterSnapshot = func() {
		email := "second@icloud.com"
		if _, err := mgr.UpdateMetadata(summary.ID, account.UpdateAccountInput{ICloudEmail: &email}); err != nil {
			t.Fatal(err)
		}
	}
	s := newWithBackend(b, Config{AdminPassword: "admin-pass-2026-strong"})
	grant := &shareGrant{AccountID: summary.ID, AliasID: "one", Email: "one@icloud.com", Boundary: f.boundary}
	grant.Boundary.ConfigurationVersion = acc.MailboxVersion
	s.shares.grants[shareKey(grant.AccountID, grant.AliasID)] = grant
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/api/shared/inbox", nil)
	s.sharedResponse(c, grant, gin.H{"body": "old-mailbox-secret"}, nil)
	if w.Code != 404 || !strings.Contains(w.Body.String(), "SHARE_UNAVAILABLE") || strings.Contains(w.Body.String(), "old-mailbox-secret") {
		t.Fatalf("configuration change leaked old result: %d %s", w.Code, w.Body.String())
	}
}

func TestShareRequestLogsDoNotRecordQueryCapabilities(t *testing.T) {
	var output bytes.Buffer
	previous := gin.DefaultWriter
	gin.DefaultWriter = &output
	defer func() { gin.DefaultWriter = previous }()
	s := newWithBackend(sharingFake(), Config{AdminPassword: "admin-pass-2026-strong"})
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/shared?token=private-capability-value", nil)
	req.Header.Set("Authorization", "Bearer also-private")
	s.Handler().ServeHTTP(recorder, req)
	if strings.Contains(output.String(), "private") || strings.Contains(output.String(), "token=") {
		t.Fatal("request logger exposed capability")
	}
}

func TestShareTokenIsolationScopeAndParameterValidation(t *testing.T) {
	f := sharingFake()
	s, ts, cookie, csrf := newSharingTest(t, f, "")
	one := createTestShare(t, ts, cookie, csrf, "a", "one")
	two := createTestShare(t, ts, cookie, csrf, "a", "two")
	three := createTestShare(t, ts, cookie, csrf, "b", "three")
	for _, test := range []struct{ token, email string }{{one, "one@icloud.com"}, {two, "two@icloud.com"}, {three, "three@icloud.com"}} {
		status, body := sharedRequest(t, ts, "GET", "/api/shared", test.token)
		if status != 200 || !strings.Contains(body, test.email) || strings.Contains(body, "account_id") {
			t.Fatalf("metadata status=%d %s", status, body)
		}
		status, body = sharedRequest(t, ts, "GET", "/api/shared/inbox?page=2&page_size=1&q=hello", test.token)
		if status != 200 || !strings.Contains(body, test.email) {
			t.Fatal(body)
		}
		f.mu.Lock()
		if f.queryEmail != test.email || f.queryBoundary.MinUID != 100 || f.query != "hello" || f.queryBoundary.CreatedAt.IsZero() {
			t.Fatal("server changed capability boundary")
		}
		f.mu.Unlock()
	}
	for _, path := range []string{"/api/shared?account_id=b", "/api/shared/inbox?alias=two@icloud.com", "/api/shared/inbox?days=90", "/api/shared/inbox?created_at=2000", "/api/shared/inbox?field=body", "/api/shared/inbox/100?account_id=b", "/api/shared/inbox?page=0", "/api/shared/inbox?page_size=101", "/api/shared/inbox?q=" + strings.Repeat("x", 257)} {
		if status, _ := sharedRequest(t, ts, "GET", path, one); status != 400 {
			t.Errorf("tampered path %q status=%d", path, status)
		}
	}
	for _, path := range []string{"/api/shared", "/api/shared?token=" + one, "/api/shared/inbox?token=" + one} {
		status, body := sharedRequest(t, ts, "GET", path, "")
		if status != 404 || !strings.Contains(body, "SHARE_UNAVAILABLE") {
			t.Fatalf("query capability accepted: %d %s", status, body)
		}
	}
	for _, method := range []string{"POST", "PUT", "PATCH", "DELETE"} {
		if status, _ := sharedRequest(t, ts, method, "/api/shared/inbox/100", one); status != 404 {
			t.Fatalf("public write %s=%d", method, status)
		}
	}
	for _, uid := range []string{"0", "99", "102", "oops", "4294967296"} {
		status, body := sharedRequest(t, ts, "GET", "/api/shared/inbox/"+uid, one)
		if status != 404 || !strings.Contains(body, "MESSAGE_NOT_FOUND") {
			t.Fatalf("outside UID=%s: %d %s", uid, status, body)
		}
	}
	if status, body := sharedRequest(t, ts, "GET", "/api/shared/inbox/100", one); status != 200 || !strings.Contains(body, "customer message") {
		t.Fatal(body)
	}
	s.shares.mu.Lock()
	s.shares.grants[shareKey("a", "one")].inFlight = 4
	s.shares.mu.Unlock()
	if status, _ := sharedRequest(t, ts, "GET", "/api/shared", one); status != 429 {
		t.Fatal("grant concurrency bound missing")
	}
	s.shares.mu.Lock()
	s.shares.grants[shareKey("a", "one")].inFlight = 0
	s.shares.mu.Unlock()
}

func TestShareRotationRevocationAndRestart(t *testing.T) {
	dir := t.TempDir()
	f := sharingFake()
	_, ts, cookie, csrf := newSharingTest(t, f, dir)
	first := createTestShare(t, ts, cookie, csrf, "a", "one")
	second := createTestShare(t, ts, cookie, csrf, "a", "one")
	if first == second {
		t.Fatal("rotation reused token")
	}
	if status, _ := sharedRequest(t, ts, "GET", "/api/shared", first); status != 404 {
		t.Fatal("old capability survived rotation")
	}
	raw, err := os.ReadFile(filepath.Join(dir, "shares.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), first) || strings.Contains(string(raw), second) || !strings.Contains(string(raw), tokenHash(second)) {
		t.Fatal("persistent capability is not hashed")
	}
	_, restarted, newCookie, newCSRF := newSharingTest(t, f, dir)
	if status, _ := sharedRequest(t, restarted, "GET", "/api/shared", second); status != 200 {
		t.Fatal("active grant did not survive restart")
	}
	status, body := shareAdminRequest(t, restarted, "DELETE", "/api/aliases/one/share", newCookie, newCSRF, `{"account_id":"a"}`)
	if status != 200 || !strings.Contains(body, `"active":false`) {
		t.Fatal(body)
	}
	_, third, cookie3, csrf3 := newSharingTest(t, f, dir)
	if status, _ := sharedRequest(t, third, "GET", "/api/shared", second); status != 404 {
		t.Fatal("revoked grant returned after restart")
	}
	f.mu.Lock()
	f.boundary.MinUID = 200
	f.mu.Unlock()
	thirdToken := createTestShare(t, third, cookie3, csrf3, "a", "one")
	if thirdToken == second {
		t.Fatal("recreation reused capability")
	}
	if status, body := sharedRequest(t, third, "GET", "/api/shared/inbox/100", thirdToken); status != 404 || !strings.Contains(body, "MESSAGE_NOT_FOUND") {
		t.Fatal("recreation retained historical UID scope")
	}
}

func TestShareIdentityAndAliasRemovalPermanentlyInvalidate(t *testing.T) {
	for _, mismatch := range []string{"mailbox", "version", "uidvalidity", "alias", "account"} {
		t.Run(mismatch, func(t *testing.T) {
			dir := t.TempDir()
			f := sharingFake()
			_, ts, cookie, csrf := newSharingTest(t, f, dir)
			token := createTestShare(t, ts, cookie, csrf, "a", "one")
			f.mu.Lock()
			switch mismatch {
			case "mailbox":
				f.boundary.MailboxIdentity = "different"
			case "version":
				f.boundary.ConfigurationVersion = "v2"
			case "uidvalidity":
				f.boundary.UIDValidity = 8
			case "alias":
				f.accountAliases["a"] = nil
			case "account":
				f.accounts = nil
			}
			f.mu.Unlock()
			if status, body := sharedRequest(t, ts, "GET", "/api/shared", token); status != 404 || !strings.Contains(body, "SHARE_UNAVAILABLE") {
				t.Fatalf("mismatch accepted: %d %s", status, body)
			}
			fresh := sharingFake()
			_, restarted, _, _ := newSharingTest(t, fresh, dir)
			if status, _ := sharedRequest(t, restarted, "GET", "/api/shared", token); status != 404 {
				t.Fatal("identity restoration revived invalidated capability")
			}
		})
	}
}

func TestSharePersistenceFailureNeverClaimsSuccess(t *testing.T) {
	for _, method := range []string{"POST", "DELETE"} {
		t.Run(method, func(t *testing.T) {
			s, ts, cookie, csrf := newSharingTest(t, sharingFake(), t.TempDir())
			token := createTestShare(t, ts, cookie, csrf, "a", "one")
			s.shares.file = t.TempDir() // atomically replacing a directory must fail
			status, body := shareAdminRequest(t, ts, method, "/api/aliases/one/share", cookie, csrf, `{"account_id":"a"}`)
			if status != 500 || !strings.Contains(body, "PERSISTENCE_ERROR") || strings.Contains(body, `"token"`) {
				t.Fatalf("failed write claimed success: %d %s", status, body)
			}
			if status, _ := sharedRequest(t, ts, "GET", "/api/shared", token); status != 404 {
				t.Fatal("failed storage did not fail closed")
			}
		})
	}
}

func TestShareRevocationDuringReadSuppressesResult(t *testing.T) {
	f := sharingFake()
	s, ts, cookie, csrf := newSharingTest(t, f, "")
	token := createTestShare(t, ts, cookie, csrf, "a", "one")
	f.started, f.release = make(chan struct{}, 4), make(chan struct{})
	result := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		req := httptest.NewRequest("GET", "/api/shared/inbox", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		recorder := httptest.NewRecorder()
		s.Handler().ServeHTTP(recorder, req)
		result <- recorder
	}()
	select {
	case <-f.started:
	case <-time.After(5 * time.Second):
		t.Fatal("read did not start")
	}
	status, body := shareAdminRequest(t, ts, "DELETE", "/api/aliases/one/share", cookie, csrf, `{"account_id":"a"}`)
	if status != 200 {
		t.Fatal(body)
	}
	close(f.release)
	select {
	case recorder := <-result:
		if recorder.Code != 404 || !strings.Contains(recorder.Body.String(), "SHARE_UNAVAILABLE") || strings.Contains(recorder.Body.String(), "after creation") {
			t.Fatalf("in-flight read leaked: %d %s", recorder.Code, recorder.Body.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("read remained blocked")
	}
}

func TestShareRateLimit(t *testing.T) {
	s, _, _, _ := newSharingTest(t, sharingFake(), "")
	for i := 0; i < 121; i++ {
		recorder := httptest.NewRecorder()
		s.Handler().ServeHTTP(recorder, httptest.NewRequest("GET", "/api/shared", nil))
		if i == 120 && recorder.Code != 429 {
			t.Fatal("public IP rate limit missing")
		}
	}
}

func TestNewRejectsMalformedPersistentShareStore(t *testing.T) {
	dir := t.TempDir()
	mgr, err := account.NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	if err := os.WriteFile(filepath.Join(dir, "shares.json"), []byte(`{"grants":[{}]}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := New(mgr, Config{AdminPassword: "admin-pass-2026-strong", DataDir: dir}); err == nil {
		t.Fatal("startup accepted malformed persistent capability")
	}
}
