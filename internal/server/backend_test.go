package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"icloud-hme/internal/account"
	"icloud-hme/internal/hme"
	"icloud-hme/internal/mail"
)

// fakeBackend 是测试用内存 Backend,记录调用,不访问网络。
type fakeBackend struct {
	accounts []account.Summary
	aliases  []hme.Alias
	inbox    InboxResult
	created  *hme.CreateResult

	addedInput   account.AddAccountInput
	updatedID    string
	updatedInput account.UpdateAccountInput
	proxyID      string
	proxyValue   string
	cookiesID    string
	cookiesValue string
	appPwdID     string
	appPwdEmail  string
	loginID      string
	loginErr     error
	removedID    string
	removedOK    bool
	removedErr   error

	aliasActID              string
	aliasActActive          bool
	aliasActErr             error
	aliasDeleteID           string
	aliasDeleteErr          error
	aliasListCalls          int
	aliasListErr            error
	listInboxCalls          int
	listInboxQuery          InboxQuery
	getMessageCalls         int
	getMessageRecipients    []string
	getMessageErr           error
	deleteMessageCalls      int
	deleteMessageRecipients []string
	deleteMessageErr        error
	reloadCount             int
}

func (f *fakeBackend) ListAccounts() []account.Summary { return f.accounts }

func (f *fakeBackend) AddAccount(in account.AddAccountInput) (account.Summary, error) {
	f.addedInput = in
	return account.Summary{ID: "acc_new", Name: in.Name, Status: "pending"}, nil
}

func (f *fakeBackend) UpdateAccount(id string, in account.UpdateAccountInput) (account.Summary, error) {
	f.updatedID, f.updatedInput = id, in
	if len(f.accounts) == 0 {
		return account.Summary{}, fmt.Errorf("fake: 更新失败")
	}
	return f.accounts[0], nil
}

func (f *fakeBackend) UpdateProxy(id, proxy string) (account.Summary, error) {
	f.proxyID, f.proxyValue = id, proxy
	if len(f.accounts) == 0 {
		return account.Summary{}, fmt.Errorf("fake: 代理更新失败")
	}
	return f.accounts[0], nil
}

func (f *fakeBackend) UpdateCookies(id, cookies string) (account.Summary, error) {
	f.cookiesID, f.cookiesValue = id, cookies
	if len(f.accounts) == 0 {
		return account.Summary{}, fmt.Errorf("fake: cookie 更新失败")
	}
	return f.accounts[0], nil
}

func (f *fakeBackend) SetAppPassword(id, email, appPassword string) (account.Summary, error) {
	f.appPwdID, f.appPwdEmail = id, email
	if len(f.accounts) == 0 {
		return account.Summary{}, fmt.Errorf("fake: 密码设置失败")
	}
	return f.accounts[0], nil
}

func (f *fakeBackend) SetMailbox(id string, config account.MailboxConfig) (account.Summary, error) {
	if len(f.accounts) == 0 {
		return account.Summary{}, fmt.Errorf("fake: 收件邮箱设置失败")
	}
	return f.accounts[0], nil
}

func (f *fakeBackend) LoginAccount(id, password, otp string) (account.Summary, error) {
	f.loginID = id
	if f.loginErr != nil {
		return account.Summary{}, f.loginErr
	}
	if len(f.accounts) == 0 {
		return account.Summary{}, fmt.Errorf("fake: 登录失败")
	}
	return f.accounts[0], nil
}

func (f *fakeBackend) RemoveAccount(id string) (bool, error) {
	f.removedID = id
	return f.removedOK, f.removedErr
}

func (f *fakeBackend) CreateAlias(accountID, label string) (*hme.CreateResult, error) {
	return f.created, nil
}

func (f *fakeBackend) ListAliases(accountID string) ([]hme.Alias, error) {
	f.aliasListCalls++
	return f.aliases, f.aliasListErr
}

func (f *fakeBackend) SetAliasActive(accountID, anonymousID string, active bool) (bool, error) {
	f.aliasActID, f.aliasActActive = anonymousID, active
	return true, f.aliasActErr
}

func (f *fakeBackend) DeleteAlias(accountID, anonymousID string) error {
	f.aliasDeleteID = anonymousID
	return f.aliasDeleteErr
}

func (f *fakeBackend) ListInbox(q InboxQuery) (InboxResult, error) {
	f.listInboxCalls++
	f.listInboxQuery = q
	return f.inbox, nil
}

func (f *fakeBackend) GetMessage(accountID string, uid uint32, recipients []string) (*mail.FullMessage, error) {
	f.getMessageCalls++
	f.getMessageRecipients = append([]string(nil), recipients...)
	return &mail.FullMessage{Message: mail.Message{ID: fmt.Sprint(uid)}}, f.getMessageErr
}

func (f *fakeBackend) DeleteMessage(accountID string, uid uint32, recipients []string) error {
	f.deleteMessageCalls++
	f.deleteMessageRecipients = append([]string(nil), recipients...)
	return f.deleteMessageErr
}

func (f *fakeBackend) Reload() error {
	f.reloadCount++
	return nil
}

func TestMapUpdateCookiesErr(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantCode   string
		wantMsg    string
	}{
		{
			name:       "账号不存在保留账号错误映射",
			err:        fmt.Errorf("账号不存在: acc_missing"),
			wantStatus: http.StatusNotFound,
			wantCode:   "ACCOUNT_NOT_FOUND",
			wantMsg:    "账号不存在",
		},
		{
			name:       "上游会话失效不泄露响应",
			err:        fmt.Errorf("Cookie 校验失败: HTTP 401: secret upstream body"),
			wantStatus: http.StatusUnauthorized,
			wantCode:   "UPSTREAM_UNAUTHORIZED",
			wantMsg:    "iCloud 会话失效,请更新 Cookie",
		},
		{
			name:       "上游网络失败使用固定文案",
			err:        fmt.Errorf("连接失败: dial tcp 192.0.2.1:443"),
			wantStatus: http.StatusBadGateway,
			wantCode:   "UPSTREAM_FAILURE",
			wantMsg:    "Cookie 校验失败",
		},
		{
			name:       "本地持久化失败使用独立错误",
			err:        &account.PersistenceError{Err: fmt.Errorf("disk full at a sensitive path")},
			wantStatus: http.StatusInternalServerError,
			wantCode:   "PERSISTENCE_ERROR",
			wantMsg:    "Cookie 保存失败",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := mapUpdateCookiesErr(tt.err)
			if got.Status != tt.wantStatus || got.Code != tt.wantCode || got.Message != tt.wantMsg {
				t.Fatalf("mapUpdateCookiesErr() = %#v, want status=%d code=%q message=%q", got, tt.wantStatus, tt.wantCode, tt.wantMsg)
			}
		})
	}
}

func TestManagerBackendRemoveAccountMapsPersistenceFailure(t *testing.T) {
	dir := t.TempDir()
	mgr, err := account.NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	summary, err := mgr.AddAccountWithInput(account.AddAccountInput{
		Name: "keep", ICloudEmail: "keep@icloud.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "accounts.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(dir); err != nil {
		t.Fatal(err)
	}

	removed, err := (&managerBackend{mgr: mgr}).RemoveAccount(summary.ID)
	if removed {
		t.Fatal("remove reported success after persistence failure")
	}
	be := asBackendError(err)
	if be.Status != http.StatusInternalServerError || be.Code != "PERSISTENCE_ERROR" {
		t.Fatalf("remove error=%#v, want 500/PERSISTENCE_ERROR", be)
	}
	if _, ok := mgr.GetAccount(summary.ID); !ok {
		t.Fatal("manager lost account after failed backend removal")
	}
}

// newTestServer 构造带固定密码与 fake backend 的测试 Server。
func newTestServer(f *fakeBackend) (*Server, *httptest.Server) {
	cfg := Config{
		Debug:         false,
		AdminPassword: "admin-pass-2026-strong",
		SessionTTL:    12 * time.Hour,
	}
	s := newWithBackend(f, cfg)
	ts := httptest.NewServer(s.Handler())
	return s, ts
}

// login 登录测试服务并返回 session Cookie 与 CSRF。
func login(t *testing.T, ts *httptest.Server, password string) (sessionCookie, csrf string) {
	t.Helper()
	body := fmt.Sprintf(`{"password":%q}`, password)
	req, _ := http.NewRequest("POST", ts.URL+"/api/auth/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Success bool `json:"success"`
		Data    struct {
			CSRFToken string `json:"csrf_token"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	for _, c := range resp.Cookies() {
		if c.Name == "hme_session" {
			return c.Value, out.Data.CSRFToken
		}
	}
	t.Fatalf("响应未设置 hme_session Cookie (status=%d)", resp.StatusCode)
	return "", ""
}

// authedReq 构造带会话 Cookie 与 CSRF 头的请求。
func authedReq(t *testing.T, ts *httptest.Server, method, path, body string) *http.Request {
	t.Helper()
	var rd *strings.Reader
	if body == "" {
		rd = strings.NewReader("")
	} else {
		rd = strings.NewReader(body)
	}
	req, _ := http.NewRequest(method, ts.URL+path, rd)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	return req
}

func do(t *testing.T, req *http.Request) (int, string, []*http.Cookie) {
	t.Helper()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return resp.StatusCode, string(raw), resp.Cookies()
}

func authedInboxRequest(t *testing.T, ts *httptest.Server, path string) (int, string) {
	t.Helper()
	session, _ := login(t, ts, "admin-pass-2026-strong")
	req := authedReq(t, ts, http.MethodGet, path, "")
	req.AddCookie(&http.Cookie{Name: "hme_session", Value: session})
	status, body, _ := do(t, req)
	return status, body
}

func TestListInboxHandlerScopesAllAliasesIncludingInactive(t *testing.T) {
	fake := &fakeBackend{
		aliases: []hme.Alias{
			{Email: "active@icloud.com", Active: true},
			{Email: "inactive@icloud.com", Active: false},
		},
		inbox: InboxResult{Messages: []mail.Message{}, Method: "imap"},
	}
	_, ts := newTestServer(fake)
	defer ts.Close()

	status, _ := authedInboxRequest(t, ts, "/api/inbox?account_id=acc_1&scope=hme_aliases&limit=20&days=7")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	want := []string{"active@icloud.com", "inactive@icloud.com"}
	if fmt.Sprint(fake.listInboxQuery.Recipients) != fmt.Sprint(want) {
		t.Fatalf("recipients = %v, want %v", fake.listInboxQuery.Recipients, want)
	}
}

func TestListInboxHandlerRejectsUnknownAliasWithoutBackendRead(t *testing.T) {
	fake := &fakeBackend{aliases: []hme.Alias{{Email: "known@icloud.com", Active: true}}}
	_, ts := newTestServer(fake)
	defer ts.Close()

	status, body := authedInboxRequest(t, ts, "/api/inbox?account_id=acc_1&alias=other%40icloud.com")
	if status != http.StatusBadRequest || !strings.Contains(body, "VALIDATION_ERROR") {
		t.Fatalf("response = %d %s, want 400 VALIDATION_ERROR", status, body)
	}
	if fake.listInboxCalls != 0 {
		t.Fatalf("ListInbox calls = %d, want 0", fake.listInboxCalls)
	}
}

func TestListInboxHandlerFailsClosedWhenAliasesUnavailable(t *testing.T) {
	fake := &fakeBackend{aliasListErr: &BackendError{Status: http.StatusBadGateway, Code: "UPSTREAM_FAILURE", Message: "获取别名失败"}}
	_, ts := newTestServer(fake)
	defer ts.Close()

	status, body := authedInboxRequest(t, ts, "/api/inbox?account_id=acc_1")
	if status != http.StatusBadGateway || !strings.Contains(body, "UPSTREAM_FAILURE") {
		t.Fatalf("response = %d %s, want alias error", status, body)
	}
	if fake.listInboxCalls != 0 {
		t.Fatalf("ListInbox calls = %d, want 0", fake.listInboxCalls)
	}
}

func TestMessageHandlersScopeDetailAndDeleteToAllAccountAliases(t *testing.T) {
	fake := &fakeBackend{aliases: []hme.Alias{
		{Email: "active@icloud.com", Active: true},
		{Email: "inactive@icloud.com", Active: false},
	}}
	_, ts := newTestServer(fake)
	defer ts.Close()

	status, _ := authedInboxRequest(t, ts, "/api/inbox/42?account_id=acc_1")
	if status != http.StatusOK {
		t.Fatalf("detail status = %d, want 200", status)
	}
	want := []string{"active@icloud.com", "inactive@icloud.com"}
	if fmt.Sprint(fake.getMessageRecipients) != fmt.Sprint(want) {
		t.Fatalf("detail recipients = %v, want %v", fake.getMessageRecipients, want)
	}

	session, csrf := login(t, ts, "admin-pass-2026-strong")
	req := authedReq(t, ts, http.MethodDelete, "/api/inbox/42?account_id=acc_1", "")
	req.AddCookie(&http.Cookie{Name: "hme_session", Value: session})
	req.Header.Set("X-CSRF-Token", csrf)
	status, _, _ = do(t, req)
	if status != http.StatusOK {
		t.Fatalf("delete status = %d, want 200", status)
	}
	if fmt.Sprint(fake.deleteMessageRecipients) != fmt.Sprint(want) {
		t.Fatalf("delete recipients = %v, want %v", fake.deleteMessageRecipients, want)
	}
}

func TestMessageHandlersFailClosedBeforeBackendWhenAliasScopeUnavailable(t *testing.T) {
	tests := []struct {
		name       string
		fake       *fakeBackend
		wantStatus int
	}{
		{name: "empty alias set", fake: &fakeBackend{}, wantStatus: http.StatusNotFound},
		{name: "alias load error", fake: &fakeBackend{aliasListErr: &BackendError{Status: http.StatusBadGateway, Code: "UPSTREAM_FAILURE", Message: "获取别名失败"}}, wantStatus: http.StatusBadGateway},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, ts := newTestServer(tc.fake)
			defer ts.Close()

			status, _ := authedInboxRequest(t, ts, "/api/inbox/42?account_id=acc_1")
			if status != tc.wantStatus {
				t.Fatalf("status = %d, want %d", status, tc.wantStatus)
			}
			if tc.fake.getMessageCalls != 0 {
				t.Fatalf("GetMessage calls = %d, want 0", tc.fake.getMessageCalls)
			}
		})
	}
}

func TestManagerBackendListInboxEmptyRecipientsReturnsEmpty(t *testing.T) {
	result, err := (&managerBackend{}).ListInbox(InboxQuery{AccountID: "acc_1"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Count != 0 || len(result.Messages) != 0 || result.Method != "imap" {
		t.Fatalf("result = %#v, want safe empty IMAP result", result)
	}
}

func TestManagerBackendListInboxReturnsStableErrorWhenFilteredIMAPUnavailable(t *testing.T) {
	mgr, err := account.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()

	_, err = (&managerBackend{mgr: mgr}).ListInbox(InboxQuery{
		AccountID:  "missing",
		Recipients: []string{"alias@icloud.com"},
	})
	backendErr := asBackendError(err)
	if backendErr.Status != http.StatusServiceUnavailable || backendErr.Code != "HME_FILTER_UNAVAILABLE" {
		t.Fatalf("error = %#v, want 503 HME_FILTER_UNAVAILABLE", backendErr)
	}
}
