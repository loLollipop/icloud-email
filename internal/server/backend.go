// Package server - 可替换业务接口与 Manager 适配器。
//
// Backend 边界固定为高层业务动作,不把具体 *hme.Client 或 *mail.Client 暴露给 handler。
package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"icloud-hme/internal/account"
	"icloud-hme/internal/hme"
	"icloud-hme/internal/mail"
)

// BackendError 是后端返回的稳定错误,携带 HTTP 状态码与稳定错误码。
type BackendError struct {
	Status  int
	Code    string
	Message string
}

func (e *BackendError) Error() string { return e.Message }

// InboxQuery 是收件箱查询参数。
type InboxQuery struct {
	AccountID   string
	Alias       string
	Recipients  []string
	Limit       int
	Days        int
	Page        int
	PageSize    int
	Search      string
	SearchField string
}

// InboxResult 是收件箱查询结果。
type InboxResult struct {
	AccountID string         `json:"account_id"`
	Alias     string         `json:"alias,omitempty"`
	Count     int            `json:"count"`
	Total     int            `json:"total"`
	Page      int            `json:"page"`
	PageSize  int            `json:"page_size"`
	Messages  []mail.Message `json:"messages"`
	Method    string         `json:"method"`
}

// Backend 是可替换的业务接口;handler 只依赖本接口,测试使用内存 fake。
type Backend interface {
	ListAccounts() []account.Summary
	AddAccount(account.AddAccountInput) (account.Summary, error)
	UpdateAccount(string, account.UpdateAccountInput) (account.Summary, error)
	UpdateProxy(string, string) (account.Summary, error)
	UpdateCookies(string, string) (account.Summary, error)
	SetAppPassword(string, string, string) (account.Summary, error)
	SetMailbox(string, account.MailboxConfig) (account.Summary, error)
	LoginAccount(string, string, string) (account.Summary, error)
	RemoveAccount(string) (bool, error)
	CreateAlias(string, string) (*hme.CreateResult, error)
	ListAliases(string) ([]hme.Alias, error)
	SetAliasActive(string, string, bool) (bool, error)
	DeleteAlias(string, string) error
	ListInbox(InboxQuery) (InboxResult, error)
	GetMessage(string, uint32, []string) (*mail.FullMessage, error)
	DeleteMessage(string, uint32, []string) error
	Reload() error
}

// managerBackend 是生产 Backend,包装 *account.Manager。
type managerBackend struct {
	mgr *account.Manager
}

// ListAccounts 返回账号安全摘要列表。
func (b *managerBackend) ListAccounts() []account.Summary {
	return b.mgr.ListSummaries()
}

// AddAccount 添加账号。
func (b *managerBackend) AddAccount(in account.AddAccountInput) (account.Summary, error) {
	sum, err := b.mgr.AddAccountWithInput(in)
	if err != nil {
		return account.Summary{}, &BackendError{Status: http.StatusBadRequest, Code: "VALIDATION_ERROR", Message: err.Error()}
	}
	return sum, nil
}

// UpdateAccount 编辑账号基本信息。
func (b *managerBackend) UpdateAccount(id string, in account.UpdateAccountInput) (account.Summary, error) {
	sum, err := b.mgr.UpdateMetadata(id, in)
	if err != nil {
		return account.Summary{}, mapAccountErr(err)
	}
	return sum, nil
}

// UpdateProxy 更新或清除账号代理。
func (b *managerBackend) UpdateProxy(id, proxy string) (account.Summary, error) {
	sum, err := b.mgr.UpdateProxy(id, proxy)
	if err != nil {
		return account.Summary{}, mapAccountErr(err)
	}
	return sum, nil
}

// UpdateCookies 更新账号 Cookie。cookies 为原始文本(Header String 或 JSON)。
func (b *managerBackend) UpdateCookies(id, cookies string) (account.Summary, error) {
	parsed, err := account.ParseCookieInput(cookies)
	if err != nil {
		return account.Summary{}, &BackendError{Status: http.StatusBadRequest, Code: "VALIDATION_ERROR", Message: err.Error()}
	}
	if err := b.mgr.UpdateCookies(id, parsed); err != nil {
		return account.Summary{}, mapUpdateCookiesErr(err)
	}
	sum, ok := b.mgr.GetAccount(id)
	if !ok {
		return account.Summary{}, &BackendError{Status: http.StatusNotFound, Code: "ACCOUNT_NOT_FOUND", Message: "账号不存在"}
	}
	return sum.Summary(), nil
}

func mapUpdateCookiesErr(err error) *BackendError {
	var persistenceErr *account.PersistenceError
	if errors.As(err, &persistenceErr) {
		return &BackendError{Status: http.StatusInternalServerError, Code: "PERSISTENCE_ERROR", Message: "Cookie 保存失败"}
	}
	if strings.Contains(err.Error(), "账号不存在") {
		return mapAccountErr(err)
	}
	return classifyUpstreamErr("Cookie 校验失败", err)
}

// SetAppPassword 设置 iCloud 邮箱与 App 专用密码并测试 IMAP 连接。
func (b *managerBackend) SetAppPassword(id, icloudEmail, appPassword string) (account.Summary, error) {
	if err := b.mgr.SetAppPassword(id, icloudEmail, appPassword); err != nil {
		msg := err.Error()
		if strings.Contains(msg, "账号不存在") {
			return account.Summary{}, &BackendError{Status: http.StatusNotFound, Code: "ACCOUNT_NOT_FOUND", Message: "账号不存在"}
		}
		if strings.Contains(msg, "不能为空") {
			return account.Summary{}, &BackendError{Status: http.StatusBadRequest, Code: "VALIDATION_ERROR", Message: msg}
		}
		// IMAP 连接失败属于上游错误,不拼接详细错误
		return account.Summary{}, &BackendError{Status: http.StatusBadGateway, Code: "UPSTREAM_FAILURE", Message: "IMAP 验证失败,请检查邮箱与 App 专用密码"}
	}
	sum, ok := b.mgr.GetAccount(id)
	if !ok {
		return account.Summary{}, &BackendError{Status: http.StatusNotFound, Code: "ACCOUNT_NOT_FOUND", Message: "账号不存在"}
	}
	return sum.Summary(), nil
}

// SetMailbox configures and verifies an external IMAP mailbox.
func (b *managerBackend) SetMailbox(id string, config account.MailboxConfig) (account.Summary, error) {
	if err := b.mgr.SetMailbox(id, config); err != nil {
		if strings.Contains(err.Error(), "账号不存在") {
			return account.Summary{}, &BackendError{Status: http.StatusNotFound, Code: "ACCOUNT_NOT_FOUND", Message: "账号不存在"}
		}
		return account.Summary{}, &BackendError{Status: http.StatusBadGateway, Code: "UPSTREAM_FAILURE", Message: "收件邮箱验证失败,请检查邮箱、授权码和 IMAP 配置"}
	}
	sum, ok := b.mgr.GetAccount(id)
	if !ok {
		return account.Summary{}, &BackendError{Status: http.StatusNotFound, Code: "ACCOUNT_NOT_FOUND", Message: "账号不存在"}
	}
	return sum.Summary(), nil
}

// LoginAccount 使用 iCloud 密码登录账号,成功只返回 Summary,绝不返回 Cookies。
func (b *managerBackend) LoginAccount(id, password, otpCode string) (account.Summary, error) {
	var otpProvider hme.OTPProvider
	if otpCode != "" {
		otp := otpCode
		otpProvider = func() (string, error) { return otp, nil }
	}

	client, err := b.mgr.HMEClientWithPassword(id, password, otpProvider)
	if err != nil {
		return account.Summary{}, classifyLoginErr(err)
	}
	_ = client
	sum, ok := b.mgr.GetAccount(id)
	if !ok {
		return account.Summary{}, &BackendError{Status: http.StatusNotFound, Code: "ACCOUNT_NOT_FOUND", Message: "账号不存在"}
	}
	return sum.Summary(), nil
}

// classifyLoginErr 把 iCloud 登录错误映射为稳定错误。
func classifyLoginErr(err error) *BackendError {
	msg := err.Error()
	if strings.Contains(msg, "需要提供 OTP") {
		return &BackendError{Status: http.StatusConflict, Code: "OTP_REQUIRED", Message: "需要提供 OTP 验证码"}
	}
	if strings.Contains(msg, "2FA 验证失败") {
		return &BackendError{Status: http.StatusUnauthorized, Code: "OTP_INVALID", Message: "OTP 验证码错误"}
	}
	if strings.Contains(msg, "账号不存在") {
		return &BackendError{Status: http.StatusNotFound, Code: "ACCOUNT_NOT_FOUND", Message: "账号不存在"}
	}
	if isSessionError(msg) {
		return &BackendError{Status: http.StatusUnauthorized, Code: "UPSTREAM_UNAUTHORIZED", Message: "iCloud 会话失效,请更新 Cookie"}
	}
	return &BackendError{Status: http.StatusBadGateway, Code: "UPSTREAM_FAILURE", Message: "iCloud 登录失败,请稍后重试"}
}

// RemoveAccount 删除账号。
func (b *managerBackend) RemoveAccount(id string) (bool, error) {
	removed, err := b.mgr.RemoveAccount(id)
	if err != nil {
		var persistenceErr *account.PersistenceError
		if errors.As(err, &persistenceErr) {
			return false, &BackendError{Status: http.StatusInternalServerError, Code: "PERSISTENCE_ERROR", Message: "账号删除保存失败"}
		}
		return false, mapAccountErr(err)
	}
	return removed, nil
}

// CreateAlias 创建 HME 别名。
func (b *managerBackend) CreateAlias(accountID, label string) (*hme.CreateResult, error) {
	var result *hme.CreateResult
	err := b.mgr.WithHMEClientAndAliasRefresh(accountID, false, func(client *hme.Client) error {
		var operationErr error
		result, operationErr = client.CreateAlias(label, 5)
		return operationErr
	})
	if err != nil {
		return nil, mapHMEOperationErr("创建邮箱失败", err)
	}
	return result, nil
}

// ListAliases 列出账号的 HME 别名。
func (b *managerBackend) ListAliases(accountID string) ([]hme.Alias, error) {
	aliases, err := b.mgr.ListAliases(accountID, false)
	if err != nil {
		return nil, mapHMEOperationErr("获取别名列表失败", err)
	}
	return aliases, nil
}

// SetAliasActive 停用或激活别名。
func (b *managerBackend) SetAliasActive(accountID, anonymousID string, active bool) (bool, error) {
	var success bool
	err := b.mgr.WithHMEClientAndAliasRefresh(accountID, false, func(client *hme.Client) error {
		var operationErr error
		if active {
			success, operationErr = client.ReactivateHME(anonymousID)
		} else {
			success, operationErr = client.DeactivateHME(anonymousID)
		}
		return operationErr
	})
	if err != nil {
		msg := "操作失败"
		if !active {
			msg = "停用失败"
		} else {
			msg = "激活失败"
		}
		return false, mapHMEOperationErr(msg, err)
	}
	return success, nil
}

// DeleteAlias 删除别名。
func (b *managerBackend) DeleteAlias(accountID, anonymousID string) error {
	err := b.mgr.WithHMEClientAndAliasRefresh(accountID, false, func(client *hme.Client) error {
		return client.Delete(anonymousID)
	})
	if err != nil {
		return mapHMEOperationErr("删除失败", err)
	}
	return nil
}

// ListInbox reads only messages addressed to the handler-verified HME aliases.
func (b *managerBackend) ListInbox(q InboxQuery) (InboxResult, error) {
	if len(q.Recipients) == 0 {
		page, pageSize := inboxResultPage(q)
		return InboxResult{AccountID: q.AccountID, Alias: q.Alias, Messages: []mail.Message{}, Method: "imap", Page: page, PageSize: pageSize}, nil
	}

	var imapMessages []mail.Message
	var searchResult mail.SearchResult
	poolErr := b.mgr.WithMailClient(q.AccountID, func(mc *mail.Client) error {
		var err error
		if q.Page > 0 {
			searchResult, err = mc.SearchByRecipients(q.Recipients, q.Page, q.PageSize, q.Search, q.SearchField, q.Days)
		} else {
			imapMessages, err = mc.FindByRecipients(q.Recipients, q.Limit, q.Days)
		}
		return err
	})
	if poolErr == nil {
		if q.Page > 0 {
			return InboxResult{
				AccountID: q.AccountID,
				Alias:     q.Alias,
				Count:     len(searchResult.Messages),
				Total:     searchResult.Total,
				Page:      searchResult.Page,
				PageSize:  searchResult.PageSize,
				Messages:  searchResult.Messages,
				Method:    "imap",
			}, nil
		}
		return InboxResult{
			AccountID: q.AccountID,
			Alias:     q.Alias,
			Count:     len(imapMessages),
			Total:     len(imapMessages),
			Page:      1,
			PageSize:  q.Limit,
			Messages:  imapMessages,
			Method:    "imap",
		}, nil
	}
	return InboxResult{}, &BackendError{Status: http.StatusServiceUnavailable, Code: "HME_FILTER_UNAVAILABLE", Message: "隐私别名邮件筛选暂不可用，请配置或检查 IMAP"}
}

func inboxResultPage(q InboxQuery) (int, int) {
	if q.Page > 0 {
		pageSize := q.PageSize
		if pageSize <= 0 {
			pageSize = 20
		}
		return 1, pageSize
	}
	return 1, q.Limit
}

func (b *managerBackend) GetMessage(accountID string, uid uint32, recipients []string) (*mail.FullMessage, error) {
	if len(recipients) == 0 {
		return nil, messageNotFoundError()
	}
	mc, err := b.mgr.MailClient(accountID)
	if err != nil {
		return nil, mapAccountErr(err)
	}
	if err := mc.Connect(); err != nil {
		return nil, &BackendError{Status: http.StatusBadGateway, Code: "UPSTREAM_FAILURE", Message: "读取邮件失败"}
	}
	defer mc.Disconnect()
	message, err := mc.GetFullForRecipients(uid, recipients)
	if err != nil {
		if errors.Is(err, mail.ErrMessageOutsideHME) {
			return nil, messageNotFoundError()
		}
		return nil, &BackendError{Status: http.StatusBadGateway, Code: "UPSTREAM_FAILURE", Message: "读取邮件详情失败"}
	}
	return message, nil
}

func (b *managerBackend) DeleteMessage(accountID string, uid uint32, recipients []string) error {
	if len(recipients) == 0 {
		return messageNotFoundError()
	}
	mc, err := b.mgr.MailClient(accountID)
	if err != nil {
		return mapAccountErr(err)
	}
	if err := mc.Connect(); err != nil {
		return &BackendError{Status: http.StatusBadGateway, Code: "UPSTREAM_FAILURE", Message: "删除邮件失败"}
	}
	defer mc.Disconnect()
	if err := mc.DeleteForRecipients(uid, recipients); err != nil {
		if errors.Is(err, mail.ErrMessageOutsideHME) {
			return messageNotFoundError()
		}
		return &BackendError{Status: http.StatusBadGateway, Code: "UPSTREAM_FAILURE", Message: "删除邮件失败"}
	}
	return nil
}

func messageNotFoundError() *BackendError {
	return &BackendError{Status: http.StatusNotFound, Code: "MESSAGE_NOT_FOUND", Message: "邮件不存在"}
}

// Reload 重新加载配置。
func (b *managerBackend) Reload() error {
	if err := b.mgr.Reload(); err != nil {
		return &BackendError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Message: "重新加载配置失败"}
	}
	return nil
}

// mapAccountErr 把账号管理器错误映射为稳定错误。
func mapAccountErr(err error) *BackendError {
	msg := err.Error()
	if strings.Contains(msg, "账号不存在") {
		return &BackendError{Status: http.StatusNotFound, Code: "ACCOUNT_NOT_FOUND", Message: "账号不存在"}
	}
	if strings.Contains(msg, "Cookie") && strings.Contains(msg, "未配置") {
		return &BackendError{Status: http.StatusBadRequest, Code: "VALIDATION_ERROR", Message: "账号未配置 Cookie"}
	}
	return &BackendError{Status: http.StatusBadRequest, Code: "VALIDATION_ERROR", Message: msg}
}

func mapHMEOperationErr(message string, err error) *BackendError {
	if strings.Contains(err.Error(), "账号不存在") || strings.Contains(err.Error(), "未配置 Cookie") {
		return mapAccountErr(err)
	}
	var persistenceErr *account.PersistenceError
	if errors.As(err, &persistenceErr) {
		return &BackendError{Status: http.StatusInternalServerError, Code: "PERSISTENCE_ERROR", Message: "Cookie 保存失败"}
	}
	return classifyUpstreamErr(message, err)
}

// classifyUpstreamErr 把上游 (iCloud) 错误映射为稳定错误,不拼接上游响应体。
func classifyUpstreamErr(fixedMsg string, err error) *BackendError {
	if err == nil {
		return nil
	}
	if isSessionError(err.Error()) {
		return &BackendError{Status: http.StatusUnauthorized, Code: "UPSTREAM_UNAUTHORIZED", Message: "iCloud 会话失效,请更新 Cookie"}
	}
	return &BackendError{Status: http.StatusBadGateway, Code: "UPSTREAM_FAILURE", Message: fixedMsg}
}

// isSessionError 判断错误是否由会话失效引起。
func isSessionError(msg string) bool {
	m := strings.ToLower(msg)
	return strings.Contains(m, "401") || strings.Contains(m, "403") ||
		strings.Contains(m, "session") || strings.Contains(m, "cookie") ||
		strings.Contains(m, "unauthorized") || strings.Contains(m, "认证") ||
		strings.Contains(m, "会话校验失败")
}

// asBackendError 提取 BackendError,非 BackendError 统一为 INTERNAL_ERROR。
func asBackendError(err error) *BackendError {
	var be *BackendError
	if errors.As(err, &be) {
		return be
	}
	return &BackendError{Status: http.StatusInternalServerError, Code: "INTERNAL_ERROR", Message: "内部错误"}
}

// cookieInputToJSON 把 handler 解析出的 map 转回 JSON 文本,交给 ParseCookieInput。
func cookieInputToJSON(cookies map[string]string) string {
	raw, err := json.Marshal(cookies)
	if err != nil {
		return ""
	}
	return string(raw)
}
