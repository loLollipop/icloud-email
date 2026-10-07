// Package server - 安全中间件:请求上限、安全响应头、CSRF 校验。
package server

import (
	"bytes"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// maxBodyBytes 是 API 请求体上限。
const maxBodyBytes = 1 << 20 // 1 MiB

// securityHeaders 是全局安全响应头。
var securityHeaders = map[string]string{
	// style-src-attr is needed by the existing React inline styles and by
	// sandboxed mail srcdoc documents, which inherit this response policy.
	// style-src-elem admits only same-origin CSS and MailHtmlFrame's exact
	// component stylesheet; arbitrary inline <style> remains blocked.
	"Content-Security-Policy": "default-src 'self'; script-src 'self'; style-src 'self'; style-src-attr 'unsafe-inline'; style-src-elem 'self' 'sha256-eKr+7RSpsPrZqhQZ1gTR40o8Ir1CG8gkheULySs/9+A='; img-src 'self' data:; connect-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'",
	"X-Content-Type-Options":  "nosniff",
	"Referrer-Policy":         "no-referrer",
	"Permissions-Policy":      "camera=(), microphone=(), geolocation=()",
}

// securityHeadersMiddleware 设置全局安全响应头。
func securityHeadersMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		for k, v := range securityHeaders {
			c.Header(k, v)
		}
		c.Next()
	}
}

// apiCacheControlMiddleware 给 API 响应设置 no-store。
func apiCacheControlMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.Next()
	}
}

// apiBodyLimitMiddleware 在任何 API handler 运行前检查完整请求体。
// 多读一个字节以识别未知长度/分块请求，内存和读取量始终有界。
func apiBodyLimitMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.ContentLength > maxBodyBytes {
			rejectOversizedBody(c)
			return
		}
		if c.Request.Body != nil {
			body, err := io.ReadAll(io.LimitReader(c.Request.Body, maxBodyBytes+1))
			if len(body) > maxBodyBytes {
				rejectOversizedBody(c)
				return
			}
			if err != nil {
				failCode(c, http.StatusBadRequest, "VALIDATION_ERROR", "请求体读取失败")
				return
			}
			_ = c.Request.Body.Close()
			c.Request.Body = io.NopCloser(bytes.NewReader(body))
		}
		c.Next()
	}
}

// Reject without draining an unfinished HTTP/1 body. The read deadline also
// bounds net/http's post-handler cleanup; Gin exposes the underlying writer.
func rejectOversizedBody(c *gin.Context) {
	if c.Request.ProtoMajor == 1 {
		c.Header("Connection", "close")
	}
	_ = http.NewResponseController(c.Writer).SetReadDeadline(time.Now())
	failCode(c, http.StatusRequestEntityTooLarge, "PAYLOAD_TOO_LARGE", "请求体不能超过 1 MiB")
}

// csrfCheck 校验状态变更请求的 CSRF token。
func csrfCheck(mgr *authManager) gin.HandlerFunc {
	return func(c *gin.Context) {
		sessionID := sessionIDFromCookie(c)
		if sessionID == "" {
			failCode(c, http.StatusForbidden, "CSRF_INVALID", "缺少会话")
			return
		}
		token := c.GetHeader("X-CSRF-Token")
		if token == "" || !mgr.ValidateCSRF(sessionID, token) {
			failCode(c, http.StatusForbidden, "CSRF_INVALID", "CSRF 校验失败")
			return
		}
		c.Next()
	}
}
