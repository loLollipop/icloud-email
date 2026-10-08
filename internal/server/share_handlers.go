package server

import (
	"crypto/rand"
	"encoding/base64"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"icloud-hme/internal/hme"
)

// Arm the deadline at the first actual response write, after upstream work.
// This also bounds errors and administrative share responses when Handler is
// embedded in an HTTP server without its own WriteTimeout.
type shareDeadlineWriter struct {
	gin.ResponseWriter
	timeout time.Duration
	started bool
}

func (w *shareDeadlineWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }
func (w *shareDeadlineWriter) start() {
	if !w.started {
		w.started = true
		// net/http supports this for HTTP/1 and HTTP/2. In-memory test writers
		// may report ErrNotSupported; they do not perform network writes.
		_ = http.NewResponseController(w.ResponseWriter).SetWriteDeadline(time.Now().Add(w.timeout))
	}
}
func (w *shareDeadlineWriter) Write(p []byte) (int, error) {
	w.start()
	return w.ResponseWriter.Write(p)
}
func (w *shareDeadlineWriter) WriteString(p string) (int, error) {
	w.start()
	return w.ResponseWriter.WriteString(p)
}
func (w *shareDeadlineWriter) WriteHeaderNow() { w.start(); w.ResponseWriter.WriteHeaderNow() }
func (w *shareDeadlineWriter) Flush()          { w.start(); w.ResponseWriter.Flush() }

func (s *Server) shareWriteDeadlineMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Request.URL.Path
		if path == "/api/shared" || strings.HasPrefix(path, "/api/shared/") || strings.HasPrefix(path, "/api/aliases/") && strings.HasSuffix(path, "/share") {
			c.Writer = &shareDeadlineWriter{ResponseWriter: c.Writer, timeout: s.shareWriteTimeout}
		}
		c.Next()
	}
}

func (s *Server) shareAlias(accountID, aliasID string) (*hme.Alias, error) {
	exists := false
	for _, account := range s.be.ListAccounts() {
		if account.ID == accountID {
			exists = true
			break
		}
	}
	if !exists {
		return nil, shareUnavailableError()
	}
	// A cached alias list is unsuitable for a public authorization decision.
	aliases, err := s.be.ListAliases(accountID)
	if err != nil {
		if asBackendError(err).Code == "ACCOUNT_NOT_FOUND" {
			return nil, shareUnavailableError()
		}
		return nil, err
	}
	for _, alias := range aliases {
		if alias.AnonymousID == aliasID && strings.TrimSpace(alias.Email) != "" {
			return &alias, nil
		}
	}
	return nil, shareUnavailableError()
}

func (s *Server) getAliasShareHandler(c *gin.Context) {
	accountID, aliasID := c.Query("account_id"), c.Param("id")
	if accountID == "" {
		failCode(c, http.StatusBadRequest, "VALIDATION_ERROR", "account_id 必填")
		return
	}
	s.shares.mu.Lock()
	grant := s.shares.grants[shareKey(accountID, aliasID)]
	storageErr := s.shares.err
	active := grant != nil && s.shares.currentLocked(grant)
	s.shares.mu.Unlock()
	if storageErr != nil {
		sharePersistenceFail(c)
		return
	}
	if active {
		ok(c, gin.H{"active": true, "created_at": shareCreatedAt(grant)})
		return
	}
	ok(c, gin.H{"active": false})
}

func (s *Server) createAliasShareHandler(c *gin.Context) {
	accountID, aliasID, valid := validateAliasAction(c)
	if !valid {
		return
	}
	alias, err := s.shareAlias(accountID, aliasID)
	if err != nil {
		backendFail(c, err)
		return
	}
	backend, available := s.be.(SharingBackend)
	if !available {
		backendFail(c, shareUnavailableError())
		return
	}
	boundary, err := backend.CaptureShareBoundary(accountID)
	if err != nil {
		backendFail(c, err)
		return
	}
	if boundary.MinUID == 0 || boundary.UIDValidity == 0 || boundary.MailboxIdentity == "" {
		backendFail(c, shareUnavailableError())
		return
	}
	// IMAP INTERNALDATE has second precision. Strictly After excludes the entire
	// creation second, conservatively hiding messages that cannot be ordered.
	boundary.CreatedAt = time.Now().UTC().Truncate(time.Second)
	random := make([]byte, 32)
	if _, err := rand.Read(random); err != nil {
		failCode(c, 500, "INTERNAL_ERROR", "生成分享链接失败")
		return
	}
	token := base64.RawURLEncoding.EncodeToString(random)
	grant := &shareGrant{AccountID: accountID, AliasID: aliasID, Email: strings.TrimSpace(alias.Email), TokenHash: tokenHash(token), Boundary: boundary}
	// Recheck the current alias and actual connected mailbox after capture.
	if err := s.validateShare(grant, nil); err != nil {
		backendFail(c, err)
		return
	}
	// The new grant is still private. Keep its delivery lock through the
	// token response, and acquire the previous grant before the global store.
	grant.delivery.Lock()
	defer grant.delivery.Unlock()
	_, unlockStore := s.shares.lockCurrent(accountID, aliasID)
	if s.shares.err != nil {
		unlockStore()
		sharePersistenceFail(c)
		return
	}
	// The final cutoff follows all upstream validation and any wait for a
	// concurrent revoke/creation, immediately before publishing the grant.
	grant.Boundary.CreatedAt = time.Now().UTC().Truncate(time.Second)
	s.shares.grants[shareKey(accountID, aliasID)] = grant
	if err := s.shares.saveLocked(); err != nil {
		s.shares.err = err
		unlockStore()
		sharePersistenceFail(c)
		return
	}
	unlockStore()
	ok(c, gin.H{"active": true, "created_at": shareCreatedAt(grant), "token": token})
	c.Writer.Flush()
}

func (s *Server) deleteAliasShareHandler(c *gin.Context) {
	accountID, aliasID, valid := validateAliasAction(c)
	if !valid {
		return
	}
	_, unlockStore := s.shares.lockCurrent(accountID, aliasID)
	if s.shares.err != nil {
		unlockStore()
		sharePersistenceFail(c)
		return
	}
	delete(s.shares.grants, shareKey(accountID, aliasID))
	if err := s.shares.saveLocked(); err != nil {
		s.shares.err = err
		unlockStore()
		sharePersistenceFail(c)
		return
	}
	unlockStore()
	ok(c, gin.H{"active": false})
}

func sharePersistenceFail(c *gin.Context) {
	failCode(c, 500, "PERSISTENCE_ERROR", "分享设置保存失败")
}

// Identity failures permanently remove the current grant, including across restart.
func (s *Server) invalidateShare(grant *shareGrant) {
	grant.delivery.Lock()
	defer grant.delivery.Unlock()
	s.shares.mu.Lock()
	defer s.shares.mu.Unlock()
	if s.shares.currentLocked(grant) {
		delete(s.shares.grants, shareKey(grant.AccountID, grant.AliasID))
		if err := s.shares.saveLocked(); err != nil {
			s.shares.err = err
		}
	}
}

func (s *Server) validateShareAlias(grant *shareGrant, knownAlias *hme.Alias) error {
	alias := knownAlias
	if alias == nil {
		var err error
		alias, err = s.shareAlias(grant.AccountID, grant.AliasID)
		if err != nil {
			if asBackendError(err).Code == "SHARE_UNAVAILABLE" {
				s.invalidateShare(grant)
				return shareUnavailableError()
			}
			// Public clients do not receive account/session diagnostics.
			return sharedUpstreamError()
		}
	}
	if alias.AnonymousID != grant.AliasID || !strings.EqualFold(strings.TrimSpace(alias.Email), grant.Email) {
		s.invalidateShare(grant)
		return shareUnavailableError()
	}
	return nil
}

func (s *Server) validateShare(grant *shareGrant, knownAlias *hme.Alias) error {
	if err := s.validateShareAlias(grant, knownAlias); err != nil {
		return err
	}
	backend, exists := s.be.(SharingBackend)
	if !exists {
		return shareUnavailableError()
	}
	if err := backend.ValidateShareBoundary(grant.AccountID, grant.Boundary); err != nil {
		if asBackendError(err).Code == "SHARE_UNAVAILABLE" {
			s.invalidateShare(grant)
		}
		return err
	}
	return nil
}

func (s *Server) sharedAccessMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if allowed, wait := s.shareLimiter.Allow(c.ClientIP()); !allowed {
			c.Header("Retry-After", strconv.Itoa(max(1, int(wait.Seconds()))))
			failCode(c, http.StatusTooManyRequests, "RATE_LIMITED", "请求过于频繁")
			return
		}
		parts := strings.Fields(c.GetHeader("Authorization"))
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || len(parts[1]) != 43 {
			backendFail(c, shareUnavailableError())
			return
		}
		decoded, err := base64.RawURLEncoding.DecodeString(parts[1])
		if err != nil || len(decoded) != 32 {
			backendFail(c, shareUnavailableError())
			return
		}
		hash := tokenHash(parts[1])
		s.shares.mu.Lock()
		var grant *shareGrant
		if s.shares.err == nil {
			for _, candidate := range s.shares.grants {
				if candidate.TokenHash == hash {
					grant = candidate
					break
				}
			}
		}
		if grant == nil {
			s.shares.mu.Unlock()
			backendFail(c, shareUnavailableError())
			return
		}
		if grant.inFlight >= 4 {
			s.shares.mu.Unlock()
			failCode(c, 429, "RATE_LIMITED", "同时请求过多")
			return
		}
		grant.inFlight++
		s.shares.mu.Unlock()
		defer func() { s.shares.mu.Lock(); grant.inFlight--; s.shares.mu.Unlock() }()
		// No client-supplied selector can affect scope; reject all unknown keys.
		for key := range c.Request.URL.Query() {
			if c.FullPath() != "/api/shared/inbox" || (key != "page" && key != "page_size" && key != "q") {
				failCode(c, 400, "VALIDATION_ERROR", "共享接口不接受该参数")
				return
			}
		}
		var validationErr error
		if c.FullPath() == "/api/shared" {
			validationErr = s.validateShare(grant, nil)
		} else {
			validationErr = s.validateShareAlias(grant, nil)
			if validationErr == nil {
				if backend, exists := s.be.(SharingBackend); exists {
					validationErr = backend.ValidateShareConfiguration(grant.AccountID, grant.Boundary)
				} else {
					validationErr = shareUnavailableError()
				}
			}
		}
		if validationErr != nil {
			if asBackendError(validationErr).Code == "SHARE_UNAVAILABLE" {
				s.invalidateShare(grant)
			}
			backendFail(c, validationErr)
			return
		}
		c.Set("share_grant", grant)
		c.Next()
	}
}

func sharedGrant(c *gin.Context) *shareGrant { return c.MustGet("share_grant").(*shareGrant) }

// Revalidate scope after upstream reads, then serialize response emission with
// revocation/rotation. A revoke that completes first cannot leak an old result.
func (s *Server) sharedResponse(c *gin.Context, grant *shareGrant, value any, operationErr error) {
	if operationErr != nil && asBackendError(operationErr).Code == "SHARE_UNAVAILABLE" {
		s.invalidateShare(grant)
	}
	if err := s.validateShareAlias(grant, nil); err != nil {
		backendFail(c, err)
		return
	}
	// Lock order is account lifecycle/read guard -> grant.delivery -> store.mu.
	// Alias queries take an account write guard and therefore run before this.
	err := s.be.(SharingBackend).WithShareConfiguration(grant.AccountID, grant.Boundary, func() error {
		grant.delivery.Lock()
		defer grant.delivery.Unlock()
		s.shares.mu.Lock()
		current := s.shares.currentLocked(grant)
		s.shares.mu.Unlock()
		if !current {
			return shareUnavailableError()
		}
		if operationErr != nil {
			return operationErr
		}
		ok(c, value)
		// Flush while the lifecycle locks are held: net/http can otherwise
		// defer a small JSON response's network write until after handler return.
		c.Writer.Flush()
		return nil
	})
	if err != nil {
		if asBackendError(err).Code == "SHARE_UNAVAILABLE" {
			s.invalidateShare(grant)
		}
		backendFail(c, err)
		return
	}
}

func (s *Server) sharedMetadataHandler(c *gin.Context) {
	grant := sharedGrant(c)
	s.sharedResponse(c, grant, gin.H{"email": grant.Email, "created_at": shareCreatedAt(grant)}, nil)
}

func (s *Server) sharedInboxHandler(c *gin.Context) {
	page, err := parseInboxInt(c.DefaultQuery("page", "1"), 1, 1_000_000)
	if err != nil {
		failCode(c, 400, "VALIDATION_ERROR", "page 无效")
		return
	}
	pageSize, err := parseInboxInt(c.DefaultQuery("page_size", "20"), 1, 100)
	if err != nil {
		failCode(c, 400, "VALIDATION_ERROR", "page_size 无效")
		return
	}
	query := strings.TrimSpace(c.Query("q"))
	if len([]rune(query)) > 256 {
		failCode(c, 400, "VALIDATION_ERROR", "q 最长 256 字符")
		return
	}
	grant := sharedGrant(c)
	result, err := s.be.(SharingBackend).SearchShared(grant.AccountID, grant.Boundary, grant.Email, page, pageSize, query)
	s.sharedResponse(c, grant, gin.H{"email": grant.Email, "created_at": shareCreatedAt(grant), "count": len(result.Messages), "total": result.Total, "page": result.Page, "page_size": result.PageSize, "messages": result.Messages}, err)
}

func (s *Server) sharedMessageHandler(c *gin.Context) {
	uid, err := strconv.ParseUint(c.Param("uid"), 10, 32)
	if err != nil || uid == 0 {
		backendFail(c, messageNotFoundError())
		return
	}
	grant := sharedGrant(c)
	message, err := s.be.(SharingBackend).GetSharedFull(grant.AccountID, grant.Boundary, grant.Email, uint32(uid))
	s.sharedResponse(c, grant, message, err)
}
