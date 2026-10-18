package server

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"icloud-hme/internal/account"
	"icloud-hme/internal/auth"
)

// TestClientIPTrustBoundary 验证只信任明确列出的代理，且不采用客户端伪造的 XFF 前缀。
func TestClientIPTrustBoundary(t *testing.T) {
	for _, tc := range []struct {
		name           string
		remoteAddr     string
		forwardedFor   string
		realIP         string
		trustedProxies []string
		want           string
	}{
		{name: "loopback IPv4 XFF", remoteAddr: "127.0.0.1:1234", forwardedFor: "203.0.113.10", want: "203.0.113.10"},
		{name: "loopback IPv6 XFF", remoteAddr: "[::1]:1234", forwardedFor: "2001:db8::10", want: "2001:db8::10"},
		{name: "loopback X-Real-IP fallback", remoteAddr: "127.0.0.1:1234", realIP: "203.0.113.10", want: "203.0.113.10"},
		{name: "XFF takes precedence", remoteAddr: "127.0.0.1:1234", forwardedFor: "203.0.113.10", realIP: "198.51.100.20", want: "203.0.113.10"},
		{name: "ignore forged XFF prefix", remoteAddr: "127.0.0.1:1234", forwardedFor: "198.51.100.20, 203.0.113.10", realIP: "198.51.100.20", want: "203.0.113.10"},
		{name: "direct IPv4 ignores headers", remoteAddr: "203.0.113.10:1234", forwardedFor: "198.51.100.20", realIP: "198.51.100.20", want: "203.0.113.10"},
		{name: "direct IPv6 ignores headers", remoteAddr: "[2001:db8::10]:1234", forwardedFor: "198.51.100.20", realIP: "198.51.100.20", want: "2001:db8::10"},
		{name: "other private peers are untrusted", remoteAddr: "192.168.1.10:1234", forwardedFor: "198.51.100.20", realIP: "198.51.100.20", want: "192.168.1.10"},
		{name: "Docker gateway untrusted by default", remoteAddr: "172.19.0.1:1234", forwardedFor: "203.0.113.10", want: "172.19.0.1"},
		{name: "explicit Docker gateway", remoteAddr: "172.19.0.1:1234", forwardedFor: "198.51.100.20, 203.0.113.10", trustedProxies: []string{"127.0.0.1", "::1", "172.19.0.1"}, want: "203.0.113.10"},
		{name: "other Docker peers are untrusted", remoteAddr: "172.19.0.3:1234", forwardedFor: "203.0.113.10", trustedProxies: []string{"172.19.0.1"}, want: "172.19.0.3"},
		{name: "invalid headers fall back to peer", remoteAddr: "127.0.0.1:1234", forwardedFor: "not-an-ip", realIP: "not-an-ip", want: "127.0.0.1"},
		{name: "explicit empty list disables trust", remoteAddr: "127.0.0.1:1234", forwardedFor: "203.0.113.10", trustedProxies: []string{}, want: "127.0.0.1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newServerForTest(t, &fakeBackend{}, Config{
				AdminPassword: "admin-pass-2026-strong", TrustedProxies: tc.trustedProxies,
			})
			s.r.GET("/test/client-ip", func(c *gin.Context) { c.String(http.StatusOK, c.ClientIP()) })
			req := httptest.NewRequest(http.MethodGet, "/test/client-ip", nil)
			req.RemoteAddr = tc.remoteAddr
			req.Header.Set("X-Forwarded-For", tc.forwardedFor)
			req.Header.Set("X-Real-IP", tc.realIP)
			recorder := httptest.NewRecorder()
			s.Handler().ServeHTTP(recorder, req)
			if recorder.Code != http.StatusOK || recorder.Body.String() != tc.want {
				t.Fatalf("ClientIP() = %q (status %d), want %q", recorder.Body.String(), recorder.Code, tc.want)
			}
		})
	}
}

func proxyLogin(s *Server, remoteAddr, forwardedFor, realIP, password string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(fmt.Sprintf(`{"password":%q}`, password)))
	req.RemoteAddr = remoteAddr
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-For", forwardedFor)
	req.Header.Set("X-Real-IP", realIP)
	recorder := httptest.NewRecorder()
	s.Handler().ServeHTTP(recorder, req)
	return recorder
}

// TestAuthRateLimitSeparatesProxiedClients 防止一个客户端锁住其他经同一反代登录的用户。
func TestAuthRateLimitSeparatesProxiedClients(t *testing.T) {
	for _, tc := range []struct {
		name           string
		remoteAddr     string
		trustedProxies []string
	}{
		{name: "loopback IPv4", remoteAddr: "127.0.0.1:1234"},
		{name: "loopback IPv6", remoteAddr: "[::1]:1234"},
		{name: "Docker gateway", remoteAddr: "172.19.0.1:1234", trustedProxies: []string{"127.0.0.1", "::1", "172.19.0.1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newServerForTest(t, &fakeBackend{}, Config{
				AdminPassword: "admin-pass-2026-strong", TrustedProxies: tc.trustedProxies,
			})
			now := time.Date(2026, time.October, 18, 0, 0, 0, 0, time.UTC)
			s.limiter = auth.NewLimiter(func() time.Time { return now }, 15*time.Minute, 5, 10000)
			for i := 0; i < 5; i++ {
				rec := proxyLogin(s, tc.remoteAddr, "203.0.113.10", "203.0.113.10", "wrong-password")
				if rec.Code != http.StatusUnauthorized {
					t.Fatalf("attempt %d: status %d: %s", i+1, rec.Code, rec.Body.String())
				}
			}
			now = now.Add(49 * time.Second)
			rec := proxyLogin(s, tc.remoteAddr, "203.0.113.10", "203.0.113.10", "admin-pass-2026-strong")
			if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "851" || len(rec.Result().Cookies()) != 0 {
				t.Fatalf("limited client: status %d, Retry-After %q: %s", rec.Code, rec.Header().Get("Retry-After"), rec.Body.String())
			}
			rec = proxyLogin(s, tc.remoteAddr, "203.0.113.11", "203.0.113.11", "admin-pass-2026-strong")
			if rec.Code != http.StatusOK || len(rec.Result().Cookies()) != 1 {
				t.Fatalf("other client should still log in: status %d: %s", rec.Code, rec.Body.String())
			}
			rec = proxyLogin(s, tc.remoteAddr, "203.0.113.10", "203.0.113.10", "admin-pass-2026-strong")
			if rec.Code != http.StatusTooManyRequests {
				t.Fatalf("other client's success cleared the blocked client: status %d", rec.Code)
			}
			now = now.Add(15*time.Minute - 49*time.Second)
			rec = proxyLogin(s, tc.remoteAddr, "203.0.113.10", "203.0.113.10", "admin-pass-2026-strong")
			if rec.Code != http.StatusOK {
				t.Fatalf("expired window should allow login: status %d: %s", rec.Code, rec.Body.String())
			}
		})
	}
}

// TestAuthRateLimitCannotBeBypassedWithForgedHeaders 保证轮换伪造头不能重置来源计数。
func TestAuthRateLimitCannotBeBypassedWithForgedHeaders(t *testing.T) {
	for _, tc := range []struct {
		name       string
		remoteAddr string
		xffSuffix  string
	}{
		{name: "direct connection", remoteAddr: "203.0.113.10:1234"},
		{name: "direct IPv6 connection", remoteAddr: "[2001:db8::10]:1234"},
		{name: "untrusted Docker peer", remoteAddr: "172.19.0.3:1234"},
		{name: "nginx appends actual client", remoteAddr: "127.0.0.1:1234", xffSuffix: ", 203.0.113.10"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newServerForTest(t, &fakeBackend{}, Config{AdminPassword: "admin-pass-2026-strong"})
			for i := 1; i <= 6; i++ {
				spoofedIP := fmt.Sprintf("198.51.100.%d", i)
				rec := proxyLogin(s, tc.remoteAddr, spoofedIP+tc.xffSuffix, spoofedIP, "wrong-password")
				want := http.StatusUnauthorized
				if i == 6 {
					want = http.StatusTooManyRequests
				}
				if rec.Code != want {
					t.Fatalf("attempt %d: status %d, want %d: %s", i, rec.Code, want, rec.Body.String())
				}
			}
		})
	}
}

func TestNewRejectsInvalidTrustedProxies(t *testing.T) {
	mgr, err := account.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mgr.Close)
	for _, proxies := range [][]string{{"127.0.0.1", "not-an-ip"}, {"172.19.0.1/99"}, {""}} {
		s, err := New(mgr, Config{AdminPassword: "admin-pass-2026-strong", TrustedProxies: proxies})
		if err == nil || s != nil {
			t.Fatalf("invalid proxies %q did not reject startup", proxies)
		}
	}
}

// TestShareRateLimitSeparatesProxiedClients 覆盖生产构造入口及共享接口的来源限流。
func TestShareRateLimitSeparatesProxiedClients(t *testing.T) {
	mgr, err := account.NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(mgr.Close)
	s, err := New(mgr, Config{
		AdminPassword:  "admin-pass-2026-strong",
		TrustedProxies: []string{"127.0.0.1", "::1", "172.19.0.1"},
	})
	if err != nil {
		t.Fatal(err)
	}
	s.shareLimiter = auth.NewLimiter(nil, time.Minute, 2, 10000)
	for _, tc := range []struct {
		client string
		status int
	}{
		{client: "203.0.113.10", status: http.StatusNotFound},
		{client: "203.0.113.10", status: http.StatusNotFound},
		{client: "203.0.113.10", status: http.StatusTooManyRequests},
		{client: "203.0.113.11", status: http.StatusNotFound},
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/shared", nil)
		req.RemoteAddr = "172.19.0.1:1234"
		req.Header.Set("X-Forwarded-For", tc.client)
		recorder := httptest.NewRecorder()
		s.Handler().ServeHTTP(recorder, req)
		if recorder.Code != tc.status {
			t.Fatalf("client %s: status %d, want %d: %s", tc.client, recorder.Code, tc.status, recorder.Body.String())
		}
	}
}
