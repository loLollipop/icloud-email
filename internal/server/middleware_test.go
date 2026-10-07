package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

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
