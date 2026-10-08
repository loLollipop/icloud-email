// Package account 实现多账号管理器。
//
// 负责账号 CRUD、Cookie 解析(Header String / JSON)、持久化到 accounts.json,
// 以及创建 HME 客户端和邮件客户端。对应原 Python 项目 account_manager.py。
package account

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"icloud-hme/internal/hme"
	"icloud-hme/internal/mail"
)

// Account 描述一个 iCloud 账号。
type Account struct {
	ID             string            `json:"id"`
	Name           string            `json:"name"`
	RealEmail      string            `json:"real_email"`
	ICloudEmail    string            `json:"icloud_email"`
	Cookies        map[string]string `json:"cookies"`
	Host           string            `json:"host"`
	Proxy          string            `json:"proxy,omitempty"` // HTTP/SOCKS5 代理
	AppPassword    string            `json:"app_password,omitempty"`
	Mailbox        *MailboxConfig    `json:"mailbox,omitempty"`
	MailboxVersion string            `json:"mailbox_version,omitempty"`
	Status         string            `json:"status"` // active / error
	AliasTotal     int               `json:"alias_total"`
	AliasActive    int               `json:"alias_active"`
	LastValidated  string            `json:"last_validated"`
	LastError      string            `json:"last_error,omitempty"`
	CreatedAt      string            `json:"created_at"`
}

// MailboxConfig describes an external mailbox used to receive forwarded mail.
type MailboxConfig struct {
	Provider string `json:"provider"`
	Email    string `json:"email"`
	IMAPHost string `json:"imap_host"`
	IMAPPort int    `json:"imap_port"`
	Password string `json:"password,omitempty"`
}

type mailClientPool interface {
	DoConfig(key, username, password, server string, port int, fn func(*mail.Client) error) error
	Drop(key string)
	Close()
}

// Manager 管理多个 iCloud 账号,线程安全。
type Manager struct {
	mu       sync.RWMutex
	accounts map[string]*Account
	dataDir  string
	dataFile string
	imapPool mailClientPool // IMAP 长连接池
	// cookieValidator is replaceable by same-package tests to deterministically
	// exercise UpdateCookies without contacting iCloud.
	cookieValidator cookieSessionValidator
	// aliasLister is replaceable by same-package tests. Production always uses
	// the authoritative HME list endpoint.
	aliasLister func(*hme.Client) ([]hme.Alias, error)
	// reloadSnapshotRead is a test synchronization hook invoked after Reload
	// has fully parsed its disk snapshot and before it waits for lifecycle users.
	reloadSnapshotRead func()

	// Configuration commits take locks in this order:
	// reloadConfigMu.RLock -> lifecycleMu.RLock -> account guard -> mu -> pool.
	// Reload instead takes reloadConfigMu.Lock -> lifecycleMu.Lock -> mu -> pool,
	// covering both its disk snapshot read and the eventual in-memory switch.
	// Session/network operations intentionally omit reloadConfigMu and start at
	// lifecycleMu so an operation already in flight can finish while Reload waits.
	reloadConfigMu sync.RWMutex
	lifecycleMu    sync.RWMutex
	accountGuardMu sync.Mutex
	accountGuards  map[string]*sync.RWMutex
}

func (m *Manager) accountGuard(id string) *sync.RWMutex {
	m.accountGuardMu.Lock()
	defer m.accountGuardMu.Unlock()
	if m.accountGuards == nil {
		m.accountGuards = make(map[string]*sync.RWMutex)
	}
	guard := m.accountGuards[id]
	if guard == nil {
		guard = &sync.RWMutex{}
		m.accountGuards[id] = guard
	}
	return guard
}

func (m *Manager) lockAccountRead(id string) func() {
	m.lifecycleMu.RLock()
	guard := m.accountGuard(id)
	guard.RLock()
	return func() {
		guard.RUnlock()
		m.lifecycleMu.RUnlock()
	}
}

func (m *Manager) lockAccount(id string) func() {
	m.lifecycleMu.RLock()
	guard := m.accountGuard(id)
	guard.Lock()
	return func() {
		guard.Unlock()
		m.lifecycleMu.RUnlock()
	}
}

func (m *Manager) lockConfigMutation() func() {
	m.reloadConfigMu.RLock()
	m.lifecycleMu.RLock()
	return func() {
		m.lifecycleMu.RUnlock()
		m.reloadConfigMu.RUnlock()
	}
}

func (m *Manager) lockAccountConfigMutation(id string) func() {
	m.reloadConfigMu.RLock()
	unlockAccount := m.lockAccount(id)
	return func() {
		unlockAccount()
		m.reloadConfigMu.RUnlock()
	}
}

// cloneCookies 返回 Cookie map 的独立副本。
func cloneCookies(cookies map[string]string) map[string]string {
	if cookies == nil {
		return nil
	}
	cloned := make(map[string]string, len(cookies))
	for k, v := range cookies {
		cloned[k] = v
	}
	return cloned
}

// copyAccount 返回账号的深拷贝(含 Cookies map),必须在持锁时调用。
func copyAccount(acc *Account) *Account {
	if acc == nil {
		return nil
	}
	cp := *acc
	cp.Cookies = cloneCookies(acc.Cookies)
	return &cp
}

// DataDirectory returns the directory used for production persistence.
func (m *Manager) DataDirectory() string { return m.dataDir }

// NewManager 创建管理器。dataDir 用于存放 accounts.json。
func NewManager(dataDir string) (*Manager, error) {
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return nil, err
	}
	m := &Manager{
		accounts:        make(map[string]*Account),
		dataDir:         dataDir,
		dataFile:        filepath.Join(dataDir, "accounts.json"),
		imapPool:        mail.NewPool(),
		cookieValidator: validateCookieSession,
		aliasLister:     func(client *hme.Client) ([]hme.Alias, error) { return client.ListAliases() },
		accountGuards:   make(map[string]*sync.RWMutex),
	}
	if err := m.load(); err != nil {
		return nil, err
	}
	return m, nil
}

// Close 释放 IMAP 连接池等资源。
func (m *Manager) Close() {
	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()
	if m.imapPool != nil {
		m.imapPool.Close()
	}
}

// Reload 重新加载 accounts.json 配置文件。
func (m *Manager) Reload() error {
	m.reloadConfigMu.Lock()
	defer m.reloadConfigMu.Unlock()

	accounts, err := m.readAccounts()
	if err != nil {
		return err
	}
	if m.reloadSnapshotRead != nil {
		m.reloadSnapshotRead()
	}

	m.lifecycleMu.Lock()
	defer m.lifecycleMu.Unlock()

	m.mu.Lock()
	if accounts != nil {
		for id, next := range accounts {
			if old := m.accounts[id]; old != nil {
				if mailboxConfiguration(old) != mailboxConfiguration(next) {
					next.MailboxVersion = uuid.NewString()
				} else {
					next.MailboxVersion = old.MailboxVersion
				}
			} else {
				next.MailboxVersion = uuid.NewString()
			}
		}
		// Persist the snapshot while the old in-memory state is still available.
		// This both repairs any stale writeback from an operation that Reload had
		// to wait for and lets a write failure leave memory and the pool untouched.
		if err := m.saveAccounts(accounts); err != nil {
			m.mu.Unlock()
			return err
		}
		m.accounts = accounts
	}
	m.mu.Unlock()
	if m.imapPool != nil {
		m.imapPool.Close()
	}
	return nil
}

func (m *Manager) load() error {
	accounts, err := m.readAccounts()
	if err != nil {
		return err
	}
	// Historically a missing file was a no-op because NewManager preinitializes
	// an empty map. Preserve that behavior for direct load callers.
	if accounts != nil {
		m.accounts = accounts
	}
	return nil
}

// readAccounts reads and parses a complete, independently owned disk snapshot.
func (m *Manager) readAccounts() (map[string]*Account, error) {
	raw, err := os.ReadFile(m.dataFile)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var wrapper struct {
		Accounts map[string]*Account `json:"accounts"`
	}
	if err := json.Unmarshal(raw, &wrapper); err != nil {
		return nil, err
	}
	if wrapper.Accounts == nil {
		wrapper.Accounts = make(map[string]*Account)
	}
	return wrapper.Accounts, nil
}

func (m *Manager) save() error {
	return m.saveAccounts(m.accounts)
}

func (m *Manager) saveAccounts(accounts map[string]*Account) error {
	wrapper := struct {
		Accounts  map[string]*Account `json:"accounts"`
		UpdatedAt string              `json:"updated_at"`
	}{
		Accounts:  accounts,
		UpdatedAt: time.Now().Format(time.RFC3339),
	}
	raw, err := json.MarshalIndent(wrapper, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(m.dataDir, ".accounts-*.tmp")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	committed := false
	defer func() {
		_ = tmp.Close()
		if !committed {
			_ = os.Remove(tmpName)
		}
	}()
	if err := tmp.Chmod(0600); err != nil {
		return err
	}
	if _, err := tmp.Write(raw); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpName, m.dataFile); err != nil {
		return err
	}
	committed = true
	// Windows does not support fsync on directory handles. The file itself has
	// already been synced and atomically replaced, so only fsync the parent on
	// platforms where directory syncing is supported.
	if runtime.GOOS != "windows" {
		dir, err := os.Open(m.dataDir)
		if err != nil {
			return err
		}
		err = dir.Sync()
		_ = dir.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// ParseCookieInput 解析 Cookie 输入,支持两种格式:
//   - Header String: "name1=value1; name2=value2; ..."
//   - JSON: {"name1":"value1","name2":"value2"}
//
// 空输入返回错误。
func ParseCookieInput(raw string) (map[string]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("空白输入 — 请粘贴 Cookie Header String 或 JSON")
	}

	// JSON 格式
	if strings.HasPrefix(raw, "{") {
		var cookies map[string]string
		if err := json.Unmarshal([]byte(raw), &cookies); err == nil && cookies != nil {
			out := make(map[string]string, len(cookies))
			for k, v := range cookies {
				if v != "" {
					out[k] = v
				}
			}
			if len(out) > 0 {
				return out, nil
			}
		}
	}

	// Header String 格式
	cookies := make(map[string]string)
	for _, part := range strings.Split(raw, ";") {
		part = strings.TrimSpace(part)
		idx := strings.Index(part, "=")
		if idx <= 0 {
			continue
		}
		name := strings.TrimSpace(part[:idx])
		value := strings.TrimSpace(part[idx+1:])
		if name != "" {
			cookies[name] = value
		}
	}
	if len(cookies) == 0 {
		return nil, fmt.Errorf("无法解析 Cookie 输入,请提供 Header String 或 JSON 格式")
	}
	return cookies, nil
}

// AddAccount 添加一个账号。cookieInput 可为空,后续可通过 /login 获取。
//
// cookieInput 支持 Header String 或 JSON。校验失败仍会保存账号(status=error),
// 方便用户后续修正 Cookie 后重新校验。
//
// 兼容入口:新调用方请使用 AddAccountWithInput。
func (m *Manager) AddAccount(name, cookieInput, host, proxy string) (*Account, error) {
	if host == "" {
		host = "icloud.com"
	}
	acc, err := m.newAccount(name, "", cookieInput, host, proxy)
	if err != nil {
		return nil, err
	}
	unlockConfig := m.lockConfigMutation()
	defer unlockConfig()

	m.mu.Lock()
	m.accounts[acc.ID] = acc
	saveErr := m.save()
	m.mu.Unlock()
	if saveErr != nil {
		return nil, saveErr
	}
	return acc, nil
}

// AddAccountWithInput 添加账号(带完整校验)。
//
// 无 Cookie 的添加路径不访问网络;有 Cookie 时在锁外对快照执行会话校验。
func (m *Manager) AddAccountWithInput(input AddAccountInput) (Summary, error) {
	name, err := validateName(input.Name)
	if err != nil {
		return Summary{}, err
	}
	if err := validateEmail(input.ICloudEmail); err != nil {
		return Summary{}, err
	}
	host, err := validateHost(input.Host)
	if err != nil {
		return Summary{}, err
	}
	proxy, err := validateProxy(input.Proxy)
	if err != nil {
		return Summary{}, err
	}
	acc, err := m.newAccount(name, input.ICloudEmail, input.CookieInput, host, proxy)
	if err != nil {
		return Summary{}, err
	}
	unlockConfig := m.lockConfigMutation()
	defer unlockConfig()

	m.mu.Lock()
	m.accounts[acc.ID] = acc
	saveErr := m.save()
	m.mu.Unlock()
	if saveErr != nil {
		return Summary{}, saveErr
	}
	return acc.Summary(), nil
}

// newAccount 构造账号;cookieInput 非空时在锁外对快照执行会话校验。
func (m *Manager) newAccount(name, icloudEmail, cookieInput, host, proxy string) (*Account, error) {
	var cookies map[string]string
	if cookieInput != "" {
		var err error
		cookies, err = ParseCookieInput(cookieInput)
		if err != nil {
			return nil, err
		}
	} else {
		cookies = make(map[string]string)
	}

	acc := &Account{
		ID:          "acc_" + uuid.New().String()[:8],
		Name:        name,
		RealEmail:   icloudEmail,
		ICloudEmail: icloudEmail,
		Cookies:     cookies,
		Host:        host,
		Proxy:       proxy,
		Status:      "pending", // 无 Cookie 时为 pending
		CreatedAt:   time.Now().Format(time.RFC3339),
	}

	// 有 Cookie 才校验会话
	if len(cookies) > 0 {
		acc.validateCookies()
	}
	return acc, nil
}

// validateCookies 用 Cookie 校验会话并填充账号身份(在锁外对快照操作)。
func (a *Account) validateCookies() {
	host := a.Host
	if host == "" {
		host = "icloud.com"
	}
	client, err := hme.NewClient(a.Cookies, host, a.Proxy, false)
	if err != nil {
		a.Status = "error"
		a.LastError = truncate(err.Error(), 300)
		return
	}
	if err := client.ValidateSession(); err != nil {
		// validate 即使失败也可能通过 Set-Cookie 刷新部分会话状态。
		a.Cookies = client.Cookies
		a.Status = "error"
		a.LastError = truncate(err.Error(), 300)
		return
	}
	// 显式接收 validate 刷新的 Cookie，不依赖传入 map 的引用关系。
	a.Cookies = client.Cookies
	a.Status = "active"
	if info := client.AccountInfo(); info != nil {
		a.RealEmail = firstNonEmpty(info.AppleID, info.PrimaryEmail)
		if a.ICloudEmail == "" {
			a.ICloudEmail = deriveICloudEmail(info)
		}
	}
	if aliases, err := client.ListAliases(); err == nil {
		setAliasStats(a, aliases)
	}
	a.LastValidated = time.Now().Format(time.RFC3339)
}

// UpdateMetadata 编辑账号基本信息(名称、iCloud 邮箱、主机),至少提供一个字段。
func (m *Manager) UpdateMetadata(id string, input UpdateAccountInput) (Summary, error) {
	if input.Name == nil && input.ICloudEmail == nil && input.Host == nil {
		return Summary{}, fmt.Errorf("至少需要提供一个可编辑字段")
	}
	var name, email, host *string
	if input.Name != nil {
		v, err := validateName(*input.Name)
		if err != nil {
			return Summary{}, err
		}
		name = &v
	}
	if input.ICloudEmail != nil {
		if err := validateEmail(*input.ICloudEmail); err != nil {
			return Summary{}, err
		}
		v := strings.TrimSpace(*input.ICloudEmail)
		email = &v
	}
	if input.Host != nil {
		v, err := validateHost(*input.Host)
		if err != nil {
			return Summary{}, err
		}
		host = &v
	}

	unlockAccount := m.lockAccountConfigMutation(id)
	defer unlockAccount()

	m.mu.Lock()
	acc, ok := m.accounts[id]
	if !ok {
		m.mu.Unlock()
		return Summary{}, fmt.Errorf("账号不存在: %s", id)
	}
	mailConfigChanged := email != nil && acc.ICloudEmail != *email
	if mailConfigChanged {
		acc.MailboxVersion = uuid.NewString()
	}
	if name != nil {
		acc.Name = *name
	}
	if email != nil {
		acc.ICloudEmail = *email
	}
	if host != nil {
		acc.Host = *host
	}
	saveErr := m.save()
	summary := acc.Summary()
	m.mu.Unlock()

	if mailConfigChanged && m.imapPool != nil {
		m.imapPool.Drop(id)
	}
	if saveErr != nil {
		return Summary{}, saveErr
	}
	return summary, nil
}

// UpdateProxy 更新或清除账号代理。空字符串表示清除。
func (m *Manager) UpdateProxy(id, proxy string) (Summary, error) {
	proxy, err := validateProxy(proxy)
	if err != nil {
		return Summary{}, err
	}
	unlockAccount := m.lockAccountConfigMutation(id)
	defer unlockAccount()

	m.mu.Lock()
	defer m.mu.Unlock()
	acc, ok := m.accounts[id]
	if !ok {
		return Summary{}, fmt.Errorf("账号不存在: %s", id)
	}
	acc.Proxy = proxy
	if err := m.save(); err != nil {
		return Summary{}, err
	}
	return acc.Summary(), nil
}

// RemoveAccount 删除账号。
func (m *Manager) RemoveAccount(id string) (bool, error) {
	unlockAccount := m.lockAccountConfigMutation(id)
	defer unlockAccount()

	m.mu.Lock()
	acc, ok := m.accounts[id]
	if !ok {
		m.mu.Unlock()
		return false, nil
	}
	delete(m.accounts, id)
	if err := m.save(); err != nil {
		m.accounts[id] = acc
		m.mu.Unlock()
		return false, &PersistenceError{Err: err}
	}
	m.mu.Unlock()
	if m.imapPool != nil {
		m.imapPool.Drop(id)
	}
	return true, nil
}

// GetAccount 返回账号深拷贝(含 Cookies),调用方可安全使用。
func (m *Manager) GetAccount(id string) (*Account, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	acc, ok := m.accounts[id]
	if !ok {
		return nil, false
	}
	return copyAccount(acc), true
}

// WithMailboxConfiguration validates a sharing generation and holds the
// account lifecycle/read guard until commit returns. A successful configuration
// update or removal therefore cannot precede delivery of an old response.
// commit must not reenter account operations or perform upstream queries.
func (m *Manager) WithMailboxConfiguration(id, version string, commit func() error) error {
	unlockAccount := m.lockAccountRead(id)
	defer unlockAccount()
	m.mu.RLock()
	acc, exists := m.accounts[id]
	valid := exists && acc.MailboxVersion == version
	m.mu.RUnlock()
	if !valid {
		return mail.ErrShareIdentityChanged
	}
	if commit != nil {
		return commit()
	}
	return nil
}

// ListAccounts 返回所有账号的深拷贝(脱敏,不含 Cookies),按活跃状态排序。
// 兼容入口:新调用方请使用 ListSummaries。
func (m *Manager) ListAccounts() []*Account {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*Account, 0, len(m.accounts))
	for _, acc := range m.accounts {
		cp := copyAccount(acc)
		cp.Cookies = nil
		cp.AppPassword = ""
		if acc.Mailbox != nil {
			mailbox := *acc.Mailbox
			mailbox.Password = ""
			cp.Mailbox = &mailbox
		}
		out = append(out, cp)
	}
	return out
}

// ListSummaries 返回所有账号的安全摘要,排序为 active → pending → error,
// 同状态按 name、id 升序。
func (m *Manager) ListSummaries() []Summary {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Summary, 0, len(m.accounts))
	for _, acc := range m.accounts {
		out = append(out, acc.Summary())
	}
	sort.SliceStable(out, func(i, j int) bool {
		ri, rj := statusRank(out[i].Status), statusRank(out[j].Status)
		if ri != rj {
			return ri < rj
		}
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// statusRank 返回状态的排序权重。
func statusRank(status string) int {
	switch status {
	case "active":
		return 0
	case "pending":
		return 1
	default:
		return 2
	}
}

// newHMEClientLocked 为已持有账号生命周期锁的调用方创建 HME 客户端。
// 必须有有效的 Cookie 才能使用 HME 功能。
func (m *Manager) newHMEClientLocked(id string, verbose bool) (*hme.Client, error) {
	m.mu.RLock()
	acc, ok := m.accounts[id]
	var snap *Account
	if ok {
		snap = copyAccount(acc)
	}
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("账号不存在: %s", id)
	}
	if len(snap.Cookies) == 0 {
		return nil, fmt.Errorf("账号未配置 Cookie，无法使用 HME 功能")
	}
	return hme.NewClient(snap.Cookies, snap.Host, snap.Proxy, verbose)
}

// WithHMEClient 在账号级独占会话中执行一次 HME 操作，并在释放会话前保存
// 服务端刷新的 Cookie。同一账号的 HME 操作、显式 Cookie 更新和登录串行，
// 不同账号之间仍可并发。
func (m *Manager) WithHMEClient(id string, verbose bool, fn func(*hme.Client) error) error {
	unlockAccount := m.lockAccount(id)
	defer unlockAccount()

	client, err := m.newHMEClientLocked(id, verbose)
	if err != nil {
		return err
	}
	operationErr := fn(client)
	if saveErr := m.saveCookiesLocked(id, client.Cookies); saveErr != nil && operationErr == nil {
		return &PersistenceError{Err: saveErr}
	}
	return operationErr
}

// ListAliases fetches the authoritative alias list and persists its enabled
// and total counts together with any session cookies refreshed by the request.
// Failed list requests never replace the last known-good counts.
func (m *Manager) ListAliases(id string, verbose bool) ([]hme.Alias, error) {
	unlockAccount := m.lockAccount(id)
	defer unlockAccount()

	client, err := m.newHMEClientLocked(id, verbose)
	if err != nil {
		return nil, err
	}
	aliases, operationErr := m.listAliases(client)
	if saveErr := m.saveHMESessionLocked(id, client.Cookies, aliases, operationErr == nil); saveErr != nil && operationErr == nil {
		return nil, &PersistenceError{Err: saveErr}
	}
	return aliases, operationErr
}

// WithHMEClientAndAliasRefresh runs a mutating HME operation and, when it
// succeeds, refreshes alias counts from the authoritative list before releasing
// the account session lock. An auxiliary refresh failure preserves the previous
// counts and does not turn an already-successful mutation into a failure.
func (m *Manager) WithHMEClientAndAliasRefresh(id string, verbose bool, fn func(*hme.Client) error) error {
	unlockAccount := m.lockAccount(id)
	defer unlockAccount()

	client, err := m.newHMEClientLocked(id, verbose)
	if err != nil {
		return err
	}
	operationErr := fn(client)
	var aliases []hme.Alias
	statsValid := false
	if operationErr == nil {
		var refreshErr error
		aliases, refreshErr = m.listAliases(client)
		statsValid = refreshErr == nil
	}
	if saveErr := m.saveHMESessionLocked(id, client.Cookies, aliases, statsValid); saveErr != nil && operationErr == nil {
		return &PersistenceError{Err: saveErr}
	}
	return operationErr
}

func (m *Manager) listAliases(client *hme.Client) ([]hme.Alias, error) {
	if m.aliasLister != nil {
		return m.aliasLister(client)
	}
	return client.ListAliases()
}

// HMEClientWithPassword 为指定账号创建一个新的 HME 客户端,使用账号密码登录。
// 登录成功后会自动获取 Cookie 并保存到账号配置。
func (m *Manager) HMEClientWithPassword(id, password string, otpProvider hme.OTPProvider) (*hme.Client, error) {
	unlockAccount := m.lockAccount(id)
	defer unlockAccount()

	m.mu.RLock()
	acc, ok := m.accounts[id]
	var snap *Account
	if ok {
		snap = copyAccount(acc)
	}
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("账号不存在: %s", id)
	}

	email := snap.ICloudEmail
	if email == "" {
		email = snap.RealEmail
	}
	if email == "" {
		return nil, fmt.Errorf("账号未设置邮箱地址")
	}

	// Never enable verbose logging for password login: the HME client debug
	// path includes request cookies and therefore must remain opt-in only.
	client, err := hme.NewClient(nil, snap.Host, snap.Proxy, false)
	if err != nil {
		return nil, err
	}

	if err := client.Login(email, password, otpProvider); err != nil {
		return nil, err
	}

	// 先保存 accountLogin 返回的 Cookie，随后通过 validate 刷新会话并再次持久化。
	// 国区与美区都走同一条刷新链路，避免只保存登录阶段的临时 token。
	if err := m.saveCookiesLocked(id, client.Cookies); err != nil {
		return nil, err
	}
	if err := client.ValidateSession(); err != nil {
		// validate 的失败响应也可能携带 Set-Cookie，尽量保留服务端最新状态。
		_ = m.saveCookiesLocked(id, client.Cookies)
		return nil, err
	}

	// 保存 validate 刷新后的 Cookie 和账号状态。
	m.mu.Lock()
	cur, ok := m.accounts[id]
	if !ok {
		m.mu.Unlock()
		return nil, fmt.Errorf("账号不存在: %s", id)
	}
	cur.Cookies = cloneCookies(client.Cookies)
	cur.Status = "active"
	cur.LastValidated = time.Now().Format(time.RFC3339)
	cur.LastError = ""
	oldRealEmail, oldICloudEmail := cur.RealEmail, cur.ICloudEmail
	if info := client.AccountInfo(); info != nil {
		cur.RealEmail = firstNonEmpty(info.AppleID, info.PrimaryEmail)
		if cur.ICloudEmail == "" {
			cur.ICloudEmail = deriveICloudEmail(info)
		}
	}
	mailConfigChanged := cur.RealEmail != oldRealEmail || cur.ICloudEmail != oldICloudEmail
	if mailConfigChanged {
		cur.MailboxVersion = uuid.NewString()
	}
	saveErr := m.save()
	m.mu.Unlock()
	if mailConfigChanged && m.imapPool != nil {
		m.imapPool.Drop(id)
	}
	if saveErr != nil {
		return nil, saveErr
	}

	return client, nil
}

// MailClient 为指定账号创建 IMAP 邮件客户端(每次新建, 不走连接池)。
// 需要事先设置 iCloud 邮箱和 App 专用密码。
// 高频读信请用 WithMailClient 复用长连接。
func (m *Manager) MailClient(id string) (*mail.Client, error) {
	m.mu.RLock()
	acc, ok := m.accounts[id]
	var snap *Account
	if ok {
		snap = copyAccount(acc)
	}
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("账号不存在: %s", id)
	}
	if snap.Mailbox != nil && snap.Mailbox.Email != "" && snap.Mailbox.Password != "" {
		return mail.NewClientWithServer(snap.Mailbox.Email, snap.Mailbox.Password, snap.Mailbox.IMAPHost, snap.Mailbox.IMAPPort), nil
	}
	imapEmail := snap.ICloudEmail
	if imapEmail == "" {
		imapEmail = snap.RealEmail
	}
	if !isICloudDomain(imapEmail) {
		return nil, fmt.Errorf("账号未设置 iCloud 邮箱 (当前: %s)", imapEmail)
	}
	if snap.AppPassword == "" {
		return nil, fmt.Errorf("账号未设置 App 专用密码")
	}
	return mail.NewClient(imapEmail, snap.AppPassword), nil
}

// WithMailClient 使用连接池中的长连接执行 fn(串行/账号级)。
// fn 返回后连接保留在池中, 不会 Logout。
func (m *Manager) WithMailClient(id string, fn func(*mail.Client) error) error {
	unlockAccount := m.lockAccountRead(id)
	defer unlockAccount()

	m.mu.RLock()
	acc, ok := m.accounts[id]
	if !ok {
		m.mu.RUnlock()
		return fmt.Errorf("账号不存在: %s", id)
	}
	pool := m.imapPool
	if pool == nil {
		m.mu.RUnlock()
		return fmt.Errorf("IMAP 连接池未初始化")
	}

	if mailbox := acc.Mailbox; mailbox != nil && mailbox.Email != "" && mailbox.Password != "" {
		username, password := mailbox.Email, mailbox.Password
		server, port := mailbox.IMAPHost, mailbox.IMAPPort
		m.mu.RUnlock()
		return pool.DoConfig(id, username, password, server, port, fn)
	}
	imapEmail := acc.ICloudEmail
	if imapEmail == "" {
		imapEmail = acc.RealEmail
	}
	appPassword := acc.AppPassword
	m.mu.RUnlock()
	if !isICloudDomain(imapEmail) {
		return fmt.Errorf("账号未设置 iCloud 邮箱 (当前: %s)", imapEmail)
	}
	if appPassword == "" {
		return fmt.Errorf("账号未设置 App 专用密码")
	}
	return pool.DoConfig(id, imapEmail, appPassword, mail.IMAPServer, mail.IMAPPort, fn)
}

// SetMailbox validates and stores an external IMAP mailbox after testing it.
func (m *Manager) SetMailbox(id string, config MailboxConfig) error {
	config.Provider = strings.TrimSpace(config.Provider)
	config.Email = strings.TrimSpace(config.Email)
	config.IMAPHost = strings.TrimSpace(config.IMAPHost)
	if config.Email == "" || config.IMAPHost == "" || config.Password == "" {
		return fmt.Errorf("收件邮箱、IMAP 服务器和授权码不能为空")
	}
	if strings.Contains(config.IMAPHost, "://") || config.IMAPPort < 1 || config.IMAPPort > 65535 {
		return fmt.Errorf("IMAP 服务器或端口无效")
	}
	m.mu.RLock()
	_, ok := m.accounts[id]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("账号不存在: %s", id)
	}
	mc := mail.NewClientWithServer(config.Email, config.Password, config.IMAPHost, config.IMAPPort)
	if err := mc.Connect(); err != nil {
		return err
	}
	_, err := mc.InboxCount()
	mc.Disconnect()
	if err != nil {
		return err
	}
	unlockAccount := m.lockAccountConfigMutation(id)
	defer unlockAccount()

	m.mu.Lock()
	acc, ok := m.accounts[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("账号不存在: %s", id)
	}
	acc.Mailbox = &config
	acc.MailboxVersion = uuid.NewString()
	saveErr := m.save()
	m.mu.Unlock()
	if m.imapPool != nil {
		m.imapPool.Drop(id)
	}
	return saveErr
}

// WebMailClient 为指定账号创建 Web 邮件客户端。
// 使用 Cookie 认证，无需 App Password。
func (m *Manager) WebMailClient(id string) (*mail.WebClient, error) {
	m.mu.RLock()
	acc, ok := m.accounts[id]
	var snap *Account
	if ok {
		snap = copyAccount(acc)
	}
	m.mu.RUnlock()
	if !ok {
		return nil, fmt.Errorf("账号不存在: %s", id)
	}
	if len(snap.Cookies) == 0 {
		return nil, fmt.Errorf("账号未配置 Cookie，无法读取邮件")
	}
	// 从 cookies 中获取 dsid
	dsid := ""
	if v, ok := snap.Cookies["X-APPLE-WEBAUTH-USER"]; ok {
		// 解析 "v=1:s=1:d=22789132008" 格式
		parts := strings.Split(v, ":d=")
		if len(parts) == 2 {
			dsid = parts[1]
		}
	}
	return mail.NewWebClient(snap.Cookies, dsid, snap.Host), nil
}

// SetAppPassword 设置 iCloud 邮箱和 App 专用密码,并测试 IMAP 连接。
func (m *Manager) SetAppPassword(id, icloudEmail, appPassword string) error {
	if icloudEmail == "" {
		return fmt.Errorf("iCloud 邮箱不能为空")
	}
	if appPassword == "" {
		return fmt.Errorf("App 专用密码不能为空")
	}

	m.mu.RLock()
	_, ok := m.accounts[id]
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("账号不存在: %s", id)
	}

	// 测试连接(锁外)
	mc := mail.NewClient(icloudEmail, appPassword)
	if err := mc.Connect(); err != nil {
		return err
	}
	count, err := mc.InboxCount()
	mc.Disconnect()
	if err != nil {
		return err
	}

	unlockAccount := m.lockAccountConfigMutation(id)
	defer unlockAccount()

	m.mu.Lock()
	acc, ok := m.accounts[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("账号不存在: %s", id)
	}
	acc.ICloudEmail = icloudEmail
	acc.AppPassword = appPassword
	acc.MailboxVersion = uuid.NewString()
	saveErr := m.save()
	m.mu.Unlock()
	if m.imapPool != nil {
		m.imapPool.Drop(id)
	}
	_ = count
	return saveErr
}

// SaveCookies 保存指定账号的最新 Cookie（HME 操作后刷新的 token）。
// 用于客户端 validate/操作过程中从 Set-Cookie 获取了新 token 后持久化。
func (m *Manager) SaveCookies(id string, cookies map[string]string) error {
	unlockAccount := m.lockAccount(id)
	defer unlockAccount()
	return m.saveCookiesLocked(id, cookies)
}

// saveCookiesLocked 在调用方持有账号生命周期独占锁时保存 Cookie。
func (m *Manager) saveCookiesLocked(id string, cookies map[string]string) error {
	return m.saveHMESessionLocked(id, cookies, nil, false)
}

// saveHMESessionLocked persists session cookies and, only for a successful
// authoritative list response, its alias statistics. The caller holds the
// per-account lifecycle lock, preventing stale writeback across credential
// updates, reloads, or account deletion.
func (m *Manager) saveHMESessionLocked(id string, cookies map[string]string, aliases []hme.Alias, statsValid bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	acc, ok := m.accounts[id]
	if !ok {
		return fmt.Errorf("账号不存在: %s", id)
	}
	acc.Cookies = cloneCookies(cookies)
	if statsValid {
		setAliasStats(acc, aliases)
	}
	return m.save()
}

func setAliasStats(acc *Account, aliases []hme.Alias) {
	acc.AliasTotal = len(aliases)
	acc.AliasActive = 0
	for _, alias := range aliases {
		if alias.Active {
			acc.AliasActive++
		}
	}
}

// UpdateCookies 更新指定账号的 Cookie,并自动校验会话有效性。
func (m *Manager) UpdateCookies(id string, cookies map[string]string) error {
	validate := m.cookieValidator
	if validate == nil {
		validate = validateCookieSession
	}
	return m.updateCookies(id, cookies, validate)
}

type cookieSessionValidator func(*Account) (*hme.Client, error)

func validateCookieSession(snap *Account) (*hme.Client, error) {
	client, err := hme.NewClient(snap.Cookies, snap.Host, snap.Proxy, false)
	if err != nil {
		return nil, err
	}
	return client, client.ValidateSession()
}

func (m *Manager) updateCookies(id string, cookies map[string]string, validate cookieSessionValidator) error {
	if len(cookies) == 0 {
		return fmt.Errorf("cookies 不能为空")
	}
	unlockAccount := m.lockAccount(id)
	defer unlockAccount()

	m.mu.RLock()
	acc, ok := m.accounts[id]
	var snap *Account
	if ok {
		snap = copyAccount(acc)
	}
	m.mu.RUnlock()
	if !ok {
		return fmt.Errorf("账号不存在: %s", id)
	}

	// 自动校验 Cookie 是否有效(锁外对快照操作)
	snap.Cookies = cookies
	if snap.Host == "" {
		snap.Host = "icloud.com"
	}
	client, validationErr := validate(snap)
	if client == nil {
		snap.Status = "error"
		snap.LastError = "创建客户端失败: " + validationErr.Error()
	} else if validationErr != nil {
		// validate 即使失败也可能通过 Set-Cookie 刷新部分会话状态。
		snap.Cookies = client.Cookies
		snap.Status = "error"
		snap.LastError = "Cookie 校验失败: " + validationErr.Error()
	} else {
		// 显式保存 validate 响应刷新的 Cookie，不依赖传入 map 的引用关系。
		snap.Cookies = client.Cookies
		snap.Status = "active"
		snap.LastValidated = time.Now().Format(time.RFC3339)
		snap.LastError = ""
		if info := client.AccountInfo(); info != nil {
			snap.RealEmail = firstNonEmpty(info.AppleID, info.PrimaryEmail)
			if snap.ICloudEmail == "" {
				snap.ICloudEmail = deriveICloudEmail(info)
			}
		}
	}

	m.mu.Lock()
	cur, ok := m.accounts[id]
	if !ok {
		m.mu.Unlock()
		return fmt.Errorf("账号不存在: %s", id)
	}
	oldRealEmail, oldICloudEmail := cur.RealEmail, cur.ICloudEmail
	cur.Cookies = snap.Cookies
	cur.Status = snap.Status
	cur.LastValidated = snap.LastValidated
	cur.LastError = snap.LastError
	cur.RealEmail = snap.RealEmail
	if cur.ICloudEmail == "" {
		cur.ICloudEmail = snap.ICloudEmail
	}
	mailConfigChanged := cur.RealEmail != oldRealEmail || cur.ICloudEmail != oldICloudEmail
	if mailConfigChanged {
		cur.MailboxVersion = uuid.NewString()
	}
	saveErr := m.save()
	m.mu.Unlock()
	if mailConfigChanged && m.imapPool != nil {
		m.imapPool.Drop(id)
	}
	if saveErr != nil {
		return &PersistenceError{Err: saveErr}
	}
	return validationErr
}

// ---- 辅助函数 ----

// mailboxConfiguration is used only in memory to detect changed reload targets.
func mailboxConfiguration(acc *Account) string {
	raw, _ := json.Marshal([]any{acc.RealEmail, acc.ICloudEmail, acc.AppPassword, acc.Mailbox})
	return string(raw)
}

// deriveICloudEmail 从账号身份推导 iCloud 邮箱地址(用于 IMAP 登录)。
//
// 规则:
//  1. primaryEmail 是 @icloud.com/@me.com/@mac.com → 直接用
//  2. appleId 是上述域名 → 直接用
//  3. appleId 是第三方邮箱(如 @qq.com) → 取 local part 拼 @icloud.com
func deriveICloudEmail(info *hme.AccountInfo) string {
	primary := strings.TrimSpace(info.PrimaryEmail)
	appleID := strings.TrimSpace(info.AppleID)

	if isICloudDomain(primary) {
		return primary
	}
	if isICloudDomain(appleID) {
		return appleID
	}
	if strings.Contains(appleID, "@") {
		local := strings.SplitN(appleID, "@", 2)[0]
		return local + "@icloud.com"
	}
	return firstNonEmpty(primary, appleID)
}

func isICloudDomain(email string) bool {
	return email != "" && (strings.Contains(email, "@icloud.com") ||
		strings.Contains(email, "@me.com") ||
		strings.Contains(email, "@mac.com"))
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
