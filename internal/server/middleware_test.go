package server

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

type countedBody struct {
	io.Reader
	readBytes int
}

func (b *countedBody) Read(p []byte) (int, error) {
	n, err := b.Reader.Read(p)
	b.readBytes += n
	return n, err
}

func (b *countedBody) Close() error { return nil }

func TestAPIBodyLimitBeforeHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, test := range []struct {
		name          string
		size          int
		contentLength int64
		wantStatus    int
	}{
		{"known oversized", maxBodyBytes + 100, maxBodyBytes + 100, http.StatusRequestEntityTooLarge},
		{"chunked oversized", maxBodyBytes + 100, -1, http.StatusRequestEntityTooLarge},
		{"underreported oversized", maxBodyBytes + 100, 1, http.StatusRequestEntityTooLarge},
		{"known legal", 16, 16, http.StatusOK},
		{"unknown legal", 16, -1, http.StatusOK},
		{"known exact boundary", maxBodyBytes, maxBodyBytes, http.StatusOK},
		{"unknown exact boundary", maxBodyBytes, -1, http.StatusOK},
		{"empty", 0, 0, http.StatusOK},
	} {
		t.Run(test.name, func(t *testing.T) {
			payload := strings.Repeat("x", test.size)
			body := &countedBody{Reader: strings.NewReader(payload)}
			called := false
			router := gin.New()
			router.Use(securityHeadersMiddleware())
			api := router.Group("/api", apiCacheControlMiddleware(), apiBodyLimitMiddleware())
			api.POST("/write", func(c *gin.Context) {
				called = true
				got, err := io.ReadAll(c.Request.Body)
				if err != nil || string(got) != payload {
					t.Errorf("handler body was not restored: len=%d, err=%v", len(got), err)
				}
				c.Status(http.StatusOK)
			})
			req := httptest.NewRequest(http.MethodPost, "/api/write", nil)
			req.Body = body
			req.ContentLength = test.contentLength
			if test.contentLength == -1 {
				req.TransferEncoding = []string{"chunked"}
			}
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, req)
			if recorder.Code != test.wantStatus {
				t.Fatalf("status=%d, want=%d: %s", recorder.Code, test.wantStatus, recorder.Body.String())
			}
			if called != (test.wantStatus == http.StatusOK) {
				t.Fatalf("handler called=%v for status=%d", called, recorder.Code)
			}
			if body.readBytes > maxBodyBytes+1 {
				t.Fatalf("unbounded body read: %d bytes", body.readBytes)
			}
			if test.contentLength > maxBodyBytes && body.readBytes != 0 {
				t.Fatalf("known oversized body should not be read: %d bytes", body.readBytes)
			}
			if recorder.Header().Get("Cache-Control") != "no-store" {
				t.Fatal("missing API no-store")
			}
			for name, value := range securityHeaders {
				if recorder.Header().Get(name) != value {
					t.Errorf("missing security header %s", name)
				}
			}
		})
	}
}

func TestAPIBodyLimitBlocksProtectedWrites(t *testing.T) {
	fake := &fakeBackend{}
	s, ts := newTestServer(fake)
	defer ts.Close()
	session, csrf := login(t, ts, "admin-pass-2026-strong")
	payload := `{"name":"oversized","icloud_email":"test@icloud.com"}` + strings.Repeat(" ", maxBodyBytes)
	for _, unknownLength := range []bool{false, true} {
		req := authedReq(t, ts, http.MethodPost, "/api/accounts", payload)
		req.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session})
		req.Header.Set("X-CSRF-Token", csrf)
		if unknownLength {
			req.ContentLength = -1
			req.TransferEncoding = []string{"chunked"}
		}
		status, body, _ := do(t, req)
		if status != http.StatusRequestEntityTooLarge {
			t.Fatalf("unknownLength=%v: status=%d: %s", unknownLength, status, body)
		}
		if fake.addedInput.Name != "" {
			t.Fatal("oversized request reached account backend")
		}
	}
	// 体积检查也必须先于会话校验。
	req := httptest.NewRequest(http.MethodPost, "/api/accounts", strings.NewReader(payload))
	recorder := httptest.NewRecorder()
	s.Handler().ServeHTTP(recorder, req)
	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("unauthenticated oversized request: status=%d", recorder.Code)
	}
}

func TestAPIBodyLimitRejectsUnfinishedChunkWithoutDraining(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(apiBodyLimitMiddleware())
	handlerCalled := make(chan struct{}, 1)
	router.POST("/api/write", func(c *gin.Context) { handlerCalled <- struct{}{} })
	connectionClosed := make(chan struct{}, 1)
	server := httptest.NewUnstartedServer(router)
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateClosed {
			connectionClosed <- struct{}{}
		}
	}
	server.Start()
	defer server.Close()
	conn, err := net.DialTimeout("tcp", server.Listener.Addr().String(), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	// Declare a larger chunk, send just enough to exceed the limit, then keep
	// the connection open without the remaining data or terminating zero chunk.
	if _, err := fmt.Fprintf(conn, "POST /api/write HTTP/1.1\r\nHost: test\r\nTransfer-Encoding: chunked\r\n\r\n%x\r\n", maxBodyBytes+256); err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(conn, strings.NewReader(strings.Repeat("x", maxBodyBytes+1))); err != nil {
		t.Fatal(err)
	}
	response, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("unfinished oversized body delayed the response: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != http.StatusRequestEntityTooLarge || !strings.Contains(string(body), "PAYLOAD_TOO_LARGE") {
		t.Fatalf("status=%d body=%s err=%v", response.StatusCode, body, err)
	}
	select {
	case <-handlerCalled:
		t.Fatal("oversized chunk reached handler")
	default:
	}
	select {
	case <-connectionClosed:
	case <-time.After(2 * time.Second):
		t.Fatal("server kept the rejected connection open while draining its body")
	}
}

func TestSecurityHeadersAllowOnlyRequiredInlineStyles(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(securityHeadersMiddleware())
	router.GET("/", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	policy := recorder.Header().Get("Content-Security-Policy")
	for _, directive := range []string{
		"style-src 'self'",
		"style-src-attr 'unsafe-inline'",
		"style-src-elem 'self' 'sha256-eKr+7RSpsPrZqhQZ1gTR40o8Ir1CG8gkheULySs/9+A='",
		"script-src 'self'",
		"connect-src 'self'",
	} {
		if !strings.Contains(policy, directive) {
			t.Errorf("CSP %q does not contain %q", policy, directive)
		}
	}
	if strings.Contains(policy, "style-src 'self' 'unsafe-inline'") {
		t.Fatalf("CSP broadly enables inline style elements: %q", policy)
	}
}
