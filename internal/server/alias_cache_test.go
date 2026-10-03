package server

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"icloud-hme/internal/account"
	"icloud-hme/internal/hme"
)

func TestAliasCacheTTLAndCopies(t *testing.T) {
	now := time.Unix(100, 0)
	cache := newAliasCache(30 * time.Second)
	cache.now = func() time.Time { return now }
	var calls int
	load := func() ([]hme.Alias, error) {
		calls++
		return []hme.Alias{{Email: "one@icloud.com"}}, nil
	}

	first, err := cache.get("acc_1", load)
	if err != nil {
		t.Fatal(err)
	}
	first[0].Email = "mutated"
	second, _ := cache.get("acc_1", load)
	if calls != 1 || second[0].Email != "one@icloud.com" {
		t.Fatalf("cache hit/copy failed: calls=%d aliases=%+v", calls, second)
	}
	now = now.Add(31 * time.Second)
	_, _ = cache.get("acc_1", load)
	if calls != 2 {
		t.Fatalf("expired cache should reload, calls=%d", calls)
	}
}

func TestAliasCacheCoalescesAndDoesNotCacheErrors(t *testing.T) {
	cache := newAliasCache(time.Minute)
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	load := func() ([]hme.Alias, error) {
		calls.Add(1)
		close(started)
		<-release
		return []hme.Alias{{Email: "one@icloud.com"}}, nil
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); _, _ = cache.get("acc_1", load) }()
	<-started
	go func() { defer wg.Done(); _, _ = cache.get("acc_1", load) }()
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("same-key requests=%d, want 1", calls.Load())
	}

	cache.invalidate("acc_1")
	badCalls := 0
	bad := func() ([]hme.Alias, error) { badCalls++; return nil, errors.New("upstream") }
	_, _ = cache.get("acc_1", bad)
	_, _ = cache.get("acc_1", bad)
	if badCalls != 2 {
		t.Fatalf("errors must not be cached, calls=%d", badCalls)
	}
}

func TestAliasCacheInvalidationIsPerAccount(t *testing.T) {
	cache := newAliasCache(time.Minute)
	loads := map[string]int{}
	for _, id := range []string{"acc_1", "acc_2"} {
		accountID := id
		_, _ = cache.get(accountID, func() ([]hme.Alias, error) {
			loads[accountID]++
			return []hme.Alias{{Email: accountID}}, nil
		})
	}
	cache.invalidate("acc_1")
	for _, id := range []string{"acc_1", "acc_2"} {
		accountID := id
		_, _ = cache.get(accountID, func() ([]hme.Alias, error) {
			loads[accountID]++
			return []hme.Alias{{Email: accountID}}, nil
		})
	}
	if loads["acc_1"] != 2 || loads["acc_2"] != 1 {
		t.Fatalf("unexpected per-account loads: %+v", loads)
	}
}

func TestAliasCachePanicUnblocksWaitersAndDoesNotPoisonKey(t *testing.T) {
	cache := newAliasCache(time.Minute)
	started := make(chan struct{})
	release := make(chan struct{})
	waiterDone := make(chan error, 1)

	go func() {
		defer func() { _ = recover() }()
		_, _ = cache.get("acc_1", func() ([]hme.Alias, error) {
			close(started)
			<-release
			panic("boom")
		})
	}()
	<-started

	// Install an expired entry whose clock hook proves the waiter has entered
	// get while the original flight still exists. The loader cannot finish
	// until release is closed, so after the hook fires the waiter must join that
	// exact flight rather than racing to create a replacement flight.
	waiterEntered := make(chan struct{})
	expiredAt := time.Unix(100, 0)
	cache.mu.Lock()
	cache.entries["acc_1"] = aliasCacheEntry{expiresAt: expiredAt}
	cache.now = func() time.Time {
		close(waiterEntered)
		return expiredAt.Add(time.Second)
	}
	cache.mu.Unlock()
	go func() {
		_, err := cache.get("acc_1", func() ([]hme.Alias, error) {
			return []hme.Alias{{Email: "should-not-run"}}, nil
		})
		waiterDone <- err
	}()
	select {
	case <-waiterEntered:
	case <-time.After(time.Second):
		t.Fatal("waiter did not enter the existing flight")
	}
	cache.mu.Lock()
	delete(cache.entries, "acc_1")
	cache.now = time.Now
	cache.mu.Unlock()
	close(release)

	select {
	case err := <-waiterDone:
		if !errors.Is(err, errAliasLoadPanicked) {
			t.Fatalf("waiter error = %v, want panic marker", err)
		}
	case <-time.After(time.Second):
		t.Fatal("waiter remained blocked after loader panic")
	}

	aliases, err := cache.get("acc_1", func() ([]hme.Alias, error) {
		return []hme.Alias{{Email: "recovered@icloud.com"}}, nil
	})
	if err != nil || len(aliases) != 1 || aliases[0].Email != "recovered@icloud.com" {
		t.Fatalf("key did not recover after panic: aliases=%+v err=%v", aliases, err)
	}
}

type credentialCacheWindowBackend struct {
	Backend

	mu        sync.Mutex
	aliases   []hme.Alias
	listCalls int

	mutationStarted chan struct{}
	mutationRelease chan struct{}
	mutationErr     error
}

func (b *credentialCacheWindowBackend) ListAliases(string) ([]hme.Alias, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.listCalls++
	return cloneAliases(b.aliases), nil
}

func (b *credentialCacheWindowBackend) setAliases(aliases []hme.Alias) {
	b.mu.Lock()
	b.aliases = cloneAliases(aliases)
	b.mu.Unlock()
}

func (b *credentialCacheWindowBackend) calls() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.listCalls
}

func (b *credentialCacheWindowBackend) mutate() (account.Summary, error) {
	close(b.mutationStarted)
	<-b.mutationRelease
	return account.Summary{ID: "acc_1", Name: "account"}, b.mutationErr
}

func (b *credentialCacheWindowBackend) UpdateCookies(string, string) (account.Summary, error) {
	return b.mutate()
}

func (b *credentialCacheWindowBackend) LoginAccount(string, string, string) (account.Summary, error) {
	return b.mutate()
}

func TestCredentialMutationInvalidatesAliasCacheAfterBackendReturns(t *testing.T) {
	tests := []struct {
		name        string
		method      string
		path        string
		body        string
		mutationErr error
		invoke      func(*Server, *gin.Context)
		wantStatus  int
	}{
		{
			name: "cookies success", method: http.MethodPut, path: "/api/accounts/acc_1/cookies",
			body: `{"cookies":"session=new"}`, invoke: (*Server).updateCookiesHandler,
			wantStatus: http.StatusOK,
		},
		{
			name: "login failure", method: http.MethodPost, path: "/api/accounts/acc_1/login",
			body: `{"password":"secret"}`, mutationErr: errors.New("login failed after changing credentials"),
			invoke: (*Server).loginAccountHandler, wantStatus: http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend := &credentialCacheWindowBackend{
				Backend:         &fakeBackend{},
				aliases:         []hme.Alias{{Email: "old@icloud.com"}},
				mutationStarted: make(chan struct{}),
				mutationRelease: make(chan struct{}),
				mutationErr:     tt.mutationErr,
			}
			s := &Server{be: backend, aliases: newAliasCache(time.Minute)}
			load := func() ([]hme.Alias, error) { return backend.ListAliases("acc_1") }
			if _, err := s.aliases.get("acc_1", load); err != nil {
				t.Fatal(err)
			}
			backend.setAliases([]hme.Alias{{Email: "during@icloud.com"}})

			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Params = gin.Params{{Key: "id", Value: "acc_1"}}
			ctx.Request = httptest.NewRequest(tt.method, tt.path, strings.NewReader(tt.body))
			ctx.Request.Header.Set("Content-Type", "application/json")
			mutationDone := make(chan struct{})
			go func() {
				defer close(mutationDone)
				tt.invoke(s, ctx)
			}()
			<-backend.mutationStarted

			during, err := s.aliases.get("acc_1", load)
			if err != nil || len(during) != 1 || during[0].Email != "during@icloud.com" {
				t.Fatalf("cache was not refilled during mutation: aliases=%+v err=%v", during, err)
			}
			backend.setAliases([]hme.Alias{{Email: "new@icloud.com"}})
			close(backend.mutationRelease)
			<-mutationDone
			if recorder.Code != tt.wantStatus {
				t.Fatalf("mutation status=%d, want %d; body=%s", recorder.Code, tt.wantStatus, recorder.Body.String())
			}

			after, err := s.aliases.get("acc_1", load)
			if err != nil || len(after) != 1 || after[0].Email != "new@icloud.com" {
				t.Fatalf("post-mutation cache remained stale: aliases=%+v err=%v", after, err)
			}
			if calls := backend.calls(); calls != 3 {
				t.Fatalf("backend alias calls=%d, want 3", calls)
			}
		})
	}
}

func TestAliasCacheInvalidateAll(t *testing.T) {
	cache := newAliasCache(time.Minute)
	for _, id := range []string{"acc_1", "acc_2"} {
		_, _ = cache.get(id, func() ([]hme.Alias, error) {
			return []hme.Alias{{Email: id}}, nil
		})
	}
	cache.invalidateAll()
	if len(cache.entries) != 0 || len(cache.flights) != 0 {
		t.Fatalf("cache not cleared: entries=%d flights=%d", len(cache.entries), len(cache.flights))
	}
}

func TestRemoveAccountInvalidatesAliasHTTPResponse(t *testing.T) {
	f := &fakeBackend{
		aliases:   []hme.Alias{{Email: "old@icloud.com"}},
		removedOK: true,
	}
	_, ts := newTestServer(f)
	defer ts.Close()
	session, csrf := login(t, ts, "admin-pass-2026-strong")

	getAliases := func() string {
		req := authedReq(t, ts, http.MethodGet, "/api/aliases?account_id=acc_1", "")
		req.AddCookie(&http.Cookie{Name: "hme_session", Value: session})
		status, body, _ := do(t, req)
		if status != http.StatusOK {
			t.Fatalf("GET aliases status=%d body=%s", status, body)
		}
		return body
	}

	if body := getAliases(); !strings.Contains(body, "old@icloud.com") {
		t.Fatalf("initial aliases missing: %s", body)
	}
	f.aliases = []hme.Alias{{Email: "new@icloud.com"}}
	remove := authedReq(t, ts, http.MethodDelete, "/api/accounts/acc_1", "")
	remove.AddCookie(&http.Cookie{Name: "hme_session", Value: session})
	remove.Header.Set("X-CSRF-Token", csrf)
	status, body, _ := do(t, remove)
	if status != http.StatusOK {
		t.Fatalf("DELETE account status=%d body=%s", status, body)
	}

	if body := getAliases(); !strings.Contains(body, "new@icloud.com") || strings.Contains(body, "old@icloud.com") {
		t.Fatalf("stale aliases returned after account removal: %s", body)
	}
	if f.aliasListCalls != 2 {
		t.Fatalf("backend alias calls=%d, want 2", f.aliasListCalls)
	}
}

func TestCredentialMutationErrorsInvalidateAliasCache(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
		body   string
		setup  func(*fakeBackend)
	}{
		{
			name: http.MethodPut + " cookies", method: http.MethodPut,
			path: "/api/accounts/acc_1/cookies", body: `{"cookies":"session=new"}`,
		},
		{
			name: http.MethodPost + " login", method: http.MethodPost,
			path: "/api/accounts/acc_1/login", body: `{"password":"secret"}`,
			setup: func(f *fakeBackend) { f.loginErr = errors.New("login changed cookies before failing") },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeBackend{aliases: []hme.Alias{{Email: "old@icloud.com"}}}
			if tt.setup != nil {
				tt.setup(f)
			}
			_, ts := newTestServer(f)
			defer ts.Close()
			session, csrf := login(t, ts, "admin-pass-2026-strong")

			getAliases := func() string {
				req := authedReq(t, ts, http.MethodGet, "/api/aliases?account_id=acc_1", "")
				req.AddCookie(&http.Cookie{Name: "hme_session", Value: session})
				status, body, _ := do(t, req)
				if status != http.StatusOK {
					t.Fatalf("GET aliases status=%d body=%s", status, body)
				}
				return body
			}

			if body := getAliases(); !strings.Contains(body, "old@icloud.com") {
				t.Fatalf("initial aliases missing: %s", body)
			}
			f.aliases = []hme.Alias{{Email: "new@icloud.com"}}
			mutation := authedReq(t, ts, tt.method, tt.path, tt.body)
			mutation.AddCookie(&http.Cookie{Name: "hme_session", Value: session})
			mutation.Header.Set("X-CSRF-Token", csrf)
			status, _, _ := do(t, mutation)
			if status < 400 {
				t.Fatalf("credential mutation status=%d, want error", status)
			}

			if body := getAliases(); !strings.Contains(body, "new@icloud.com") || strings.Contains(body, "old@icloud.com") {
				t.Fatalf("stale aliases returned after failed credential mutation: %s", body)
			}
			if f.aliasListCalls != 2 {
				t.Fatalf("backend alias calls=%d, want 2", f.aliasListCalls)
			}
		})
	}
}
