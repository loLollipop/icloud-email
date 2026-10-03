// Package mail 实现 iCloud 邮件 IMAP 读取客户端。
//
// 通过 Apple 应用专用密码连接 imap.mail.me.com:993,
// 拉取隐私邮箱别名收到的邮件。对应原 Python 项目 icloud_mail.py。
package mail

import (
	"crypto/tls"
	"errors"
	"fmt"
	"mime"
	"net"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap"
	uidplus "github.com/emersion/go-imap-uidplus"
	"github.com/emersion/go-imap/client"
	"github.com/emersion/go-message/charset"
)

const (
	IMAPServer            = "imap.mail.me.com"
	IMAPPort              = 993
	defaultConnectTimeout = 10 * time.Second
	defaultCommandTimeout = 30 * time.Second
	searchEnvelopeBatch   = 200
	searchPreviewBytes    = 32 * 1024
)

// Variables keep timeout regression tests fast while production uses the
// explicit defaults above. Tests in this package must restore them after use.
var (
	imapConnectTimeout = defaultConnectTimeout
	imapCommandTimeout = defaultCommandTimeout
	// ErrMessageOutsideHME deliberately conflates an unknown UID with a message
	// outside the verified Hide My Email alias set.
	ErrMessageOutsideHME = errors.New("邮件不存在")
)

// Message 是一封邮件的摘要信息。
type Message struct {
	ID      string `json:"id"`
	From    string `json:"from"`
	To      string `json:"to"`
	Subject string `json:"subject"`
	Date    string `json:"date"`
	Preview string `json:"preview"`
}

// FullMessage 是一封邮件的完整内容(含正文)。
type FullMessage struct {
	Message
	Body          string `json:"body"`
	HTMLBody      string `json:"html_body,omitempty"`
	BodyTruncated bool   `json:"body_truncated,omitempty"`
	ContentType   string `json:"content_type"`
}

// SearchResult is a fully verified page of messages addressed to HME aliases.
// Total is computed after checking every SEARCH candidate's parsed envelope.
type SearchResult struct {
	Messages []Message
	Total    int
	Page     int
	PageSize int
}

// Client 是 iCloud 邮件 IMAP 客户端。
type Client struct {
	username  string
	password  string
	server    string
	port      int
	tlsConfig *tls.Config
	cli       *client.Client
}

// bootstrapDeadlineConn keeps the absolute connection deadline in force while
// go-imap reads the greeting and, when necessary, issues its initial CAPABILITY
// command. client.New has no timeout parameter and clears an existing deadline
// before that command because Client.Timeout is still zero.
type bootstrapDeadlineConn struct {
	net.Conn

	mu     sync.Mutex
	active bool
}

func (c *bootstrapDeadlineConn) SetDeadline(deadline time.Time) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.active && deadline.IsZero() {
		return nil
	}
	return c.Conn.SetDeadline(deadline)
}

func (c *bootstrapDeadlineConn) finish() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.active = false
	return c.Conn.SetDeadline(time.Time{})
}

// NewClient 创建 IMAP 客户端。需在调用其它方法前先 Connect。
func NewClient(appleID, appPassword string) *Client {
	return NewClientWithServer(appleID, appPassword, IMAPServer, IMAPPort)
}

// NewClientWithServer creates an IMAP client for a custom server.
func NewClientWithServer(username, password, server string, port int) *Client {
	return &Client{username: username, password: password, server: server, port: port}
}

// Connect 连接并登录 IMAP 服务器。已连接且存活时直接复用。
func (c *Client) Connect() error {
	if c.cli != nil {
		if err := c.cli.Noop(); err == nil {
			return nil
		}
		c.forceClose()
	}
	addr := net.JoinHostPort(c.server, strconv.Itoa(c.port))
	connectDeadline := time.Time{}
	if imapConnectTimeout > 0 {
		connectDeadline = time.Now().Add(imapConnectTimeout)
	}
	dialer := &net.Dialer{Timeout: imapConnectTimeout}
	rawConn, err := dialer.Dial("tcp", addr)
	if err != nil {
		return fmt.Errorf("IMAP 连接失败: %w", err)
	}

	tlsConfig := c.tlsConfig
	if tlsConfig == nil {
		tlsConfig = &tls.Config{}
	} else {
		tlsConfig = tlsConfig.Clone()
	}
	if tlsConfig.ServerName == "" {
		tlsConfig.ServerName = c.server
	}
	tlsConn := tls.Client(rawConn, tlsConfig)
	bootstrapConn := &bootstrapDeadlineConn{Conn: tlsConn, active: !connectDeadline.IsZero()}
	if !connectDeadline.IsZero() {
		if err := bootstrapConn.SetDeadline(connectDeadline); err != nil {
			_ = rawConn.Close()
			return fmt.Errorf("IMAP 连接失败: %w", err)
		}
	}

	cli, err := client.New(bootstrapConn)
	if err != nil {
		_ = bootstrapConn.Close()
		return fmt.Errorf("IMAP 连接失败: %w", err)
	}
	if err := bootstrapConn.finish(); err != nil {
		_ = cli.Terminate()
		return fmt.Errorf("IMAP 连接失败: %w", err)
	}
	cli.Timeout = imapCommandTimeout
	if err := cli.Login(c.username, c.password); err != nil {
		_ = cli.Terminate()
		return fmt.Errorf("IMAP 登录失败 — 请检查邮箱账号、授权码和服务器地址: %w", err)
	}
	c.cli = cli
	return nil
}

// Ping 探测连接是否仍可用(NOOP)。
func (c *Client) Ping() error {
	if c.cli == nil {
		return fmt.Errorf("未连接")
	}
	return c.cli.Noop()
}

// Disconnect 登出并关闭连接。
func (c *Client) Disconnect() {
	if c.cli != nil {
		_ = c.cli.Logout()
		c.cli = nil
	}
}

// forceClose 不发 LOGOUT, 直接掐断(坏连接/池丢弃时用)。
func (c *Client) forceClose() {
	if c.cli != nil {
		_ = c.cli.Terminate()
		c.cli = nil
	}
}

// InboxCount 返回收件箱邮件总数。
func (c *Client) InboxCount() (int, error) {
	if c.cli == nil {
		return 0, fmt.Errorf("未连接")
	}
	mbox, err := c.cli.Select("INBOX", false)
	if err != nil {
		return 0, err
	}
	return int(mbox.Messages), nil
}

// ListInbox 拉取收件箱最近 limit 封邮件摘要。
//
// days 用于过滤只看近 N 天的邮件(0 表示不限制)。
// 返回按时间倒序排列。
func (c *Client) ListInbox(limit int, days int) ([]Message, error) {
	if c.cli == nil {
		return nil, fmt.Errorf("未连接")
	}
	if limit <= 0 {
		limit = 50
	}

	mbox, err := c.cli.Select("INBOX", true)
	if err != nil {
		return nil, err
	}
	total := int(mbox.Messages)
	if total == 0 {
		return []Message{}, nil
	}

	// 计算起始序号(只取最近 limit 封)
	from := uint32(1)
	if uint32(limit) < mbox.Messages {
		from = mbox.Messages - uint32(limit) + 1
	}

	seqset := new(imap.SeqSet)
	seqset.AddRange(from, mbox.Messages)

	// 拉取完整正文,以便填充 Preview(OTP 验证码在正文中); PEEK 不标已读
	section := &imap.BodySectionName{Peek: true}
	items := []imap.FetchItem{
		imap.FetchUid,
		imap.FetchEnvelope,
		imap.FetchInternalDate,
		section.FetchItem(),
	}

	messages := make(chan *imap.Message, limit)
	done := make(chan error, 1)
	go func() {
		done <- c.cli.Fetch(seqset, items, messages)
	}()

	out := make([]Message, 0, limit)
	for msg := range messages {
		m := toMessageWithBody(msg)
		// toMessageWithBody 统一输出 RFC3339；同时兼容历史或上游返回的 RFC1123 日期。
		if !messageWithinDays(m.Date, days, time.Now()) {
			continue
		}
		out = append(out, m)
	}
	if err := <-done; err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Date > out[j].Date })
	return out, nil
}

func messageWithinDays(raw string, days int, now time.Time) bool {
	if days <= 0 {
		return true
	}
	var parsed time.Time
	var err error
	for _, layout := range []string{time.RFC3339, time.RFC1123Z, time.RFC1123} {
		parsed, err = time.Parse(layout, raw)
		if err == nil {
			break
		}
	}
	// 日期未知时保留邮件，避免因异常上游格式静默丢信。
	if err != nil {
		return true
	}
	return !parsed.Before(now.Add(-time.Duration(days) * 24 * time.Hour))
}

// FindByRecipient 查找发给指定隐私邮箱别名的最近 limit 封邮件(新→旧)。
//
// 先尝试 IMAP TO 搜索; 失败则只扫收件箱最近若干封本地过滤。
func (c *Client) FindByRecipient(recipient string, limit int, days int) ([]Message, error) {
	var out []Message
	err := c.ForEachByRecipient(recipient, limit, days, func(m Message) bool {
		out = append(out, m)
		return true // 收满 limit 为止
	})
	return out, err
}

// FindByRecipients returns the newest messages addressed to any of the exact
// recipient addresses. An empty recipient set is deliberately safe: it never
// falls back to reading the unfiltered inbox.
func (c *Client) FindByRecipients(recipients []string, limit int, days int) ([]Message, error) {
	recipients = normalizeRecipients(recipients)
	if len(recipients) == 0 {
		return []Message{}, nil
	}
	if c.cli == nil {
		return nil, fmt.Errorf("未连接")
	}
	if limit <= 0 {
		limit = 50
	}
	if _, err := c.cli.Select("INBOX", true); err != nil {
		return nil, err
	}

	criteria := recipientSearchCriteria(recipients, days, time.Now())
	uids, err := c.cli.UidSearch(criteria)
	if err == nil {
		recipientSet := make(map[string]struct{}, len(recipients))
		for _, recipient := range recipients {
			recipientSet[recipient] = struct{}{}
		}
		return c.fetchMatchingUIDs(normalizeUIDs(uids, 0), recipientSet, limit)
	}

	// Do not return a partial local scan as a successful HME inbox. Callers
	// must see that complete server-side filtering was unavailable.
	return nil, err
}

// SearchByRecipients searches the complete INBOX and returns one page after
// rechecking every server-side SEARCH candidate against the parsed envelope.
// Only the requested page has its body downloaded for preview generation.
func (c *Client) SearchByRecipients(recipients []string, page, pageSize int, query, field string, days int) (SearchResult, error) {
	recipients = normalizeRecipients(recipients)
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	empty := SearchResult{Messages: []Message{}, Page: 1, PageSize: pageSize}
	if len(recipients) == 0 {
		return empty, nil
	}
	if c.cli == nil {
		return SearchResult{}, fmt.Errorf("未连接")
	}
	if _, err := c.cli.Select("INBOX", true); err != nil {
		return SearchResult{}, err
	}

	criteria := recipientTextSearchCriteria(recipients, strings.TrimSpace(query), field, days, time.Now())
	uids, err := c.cli.UidSearch(criteria)
	if err != nil {
		return SearchResult{}, err
	}
	uids = normalizeUIDs(uids, 0)
	recipientSet := make(map[string]struct{}, len(recipients))
	for _, recipient := range recipients {
		recipientSet[recipient] = struct{}{}
	}
	verified, err := c.fetchVerifiedEnvelopeUIDs(uids, recipientSet)
	if err != nil {
		return SearchResult{}, err
	}

	total := len(verified)
	totalPages := 1
	if total > 0 {
		totalPages = (total + pageSize - 1) / pageSize
	}
	if page > totalPages {
		page = totalPages
	}
	start := (page - 1) * pageSize
	end := min(start+pageSize, total)
	pageUIDs := make([]uint32, 0, end-start)
	for i := start; i < end; i++ {
		pageUIDs = append(pageUIDs, verified[total-1-i])
	}

	messages, err := c.fetchSearchPage(pageUIDs, recipientSet)
	if err != nil {
		return SearchResult{}, err
	}
	return SearchResult{Messages: messages, Total: total, Page: page, PageSize: pageSize}, nil
}

// Fetch a page in one command, bounding preview transfer without downloading
// attachments. Full reading still uses GetFullForRecipients separately.
func (c *Client) fetchSearchPage(uids []uint32, recipients map[string]struct{}) ([]Message, error) {
	if len(uids) == 0 {
		return []Message{}, nil
	}
	seqset := new(imap.SeqSet)
	requested := make(map[uint32]struct{}, len(uids))
	for _, uid := range uids {
		seqset.AddNum(uid)
		requested[uid] = struct{}{}
	}
	section := &imap.BodySectionName{Peek: true, Partial: []int{0, searchPreviewBytes}}
	items := []imap.FetchItem{imap.FetchUid, imap.FetchEnvelope, imap.FetchInternalDate, section.FetchItem()}
	responses := make(chan *imap.Message, len(uids))
	done := make(chan error, 1)
	go func() { done <- c.cli.UidFetch(seqset, items, responses) }()
	selected := make(map[uint32]*imap.Message, len(uids))
	for msg := range responses {
		if msg == nil || msg.Envelope == nil || msg.GetBody(section) == nil {
			continue
		}
		if _, ok := requested[msg.Uid]; !ok {
			continue
		}
		if selected[msg.Uid] == nil {
			selected[msg.Uid] = msg
		}
	}
	if err := <-done; err != nil {
		return nil, err
	}
	result := make([]Message, 0, len(uids))
	for _, uid := range uids {
		raw := selected[uid]
		if raw == nil {
			return nil, fmt.Errorf("%w: 邮件预览不完整 (uid=%d)", ErrMessageOutsideHME, uid)
		}
		if !envelopeMatchesRecipients(raw.Envelope, recipients) {
			return nil, ErrMessageOutsideHME
		}
		message := toMessage(raw)
		if parsed, err := parseRFC822(raw.GetBody(section)); err == nil {
			message.Preview = truncateRunes(strings.TrimSpace(parsed.body), previewRuneLimit)
		}
		result = append(result, message)
	}
	return result, nil
}

func normalizeRecipients(recipients []string) []string {
	seen := make(map[string]struct{}, len(recipients))
	out := make([]string, 0, len(recipients))
	for _, recipient := range recipients {
		recipient = strings.ToLower(strings.TrimSpace(recipient))
		if recipient == "" {
			continue
		}
		if _, exists := seen[recipient]; exists {
			continue
		}
		seen[recipient] = struct{}{}
		out = append(out, recipient)
	}
	return out
}

func recipientSearchCriteria(recipients []string, days int, now time.Time) *imap.SearchCriteria {
	var build func(int, int) *imap.SearchCriteria
	build = func(start, end int) *imap.SearchCriteria {
		if end-start == 1 {
			criteria := imap.NewSearchCriteria()
			criteria.Header.Add("To", recipients[start])
			return criteria
		}
		middle := start + (end-start)/2
		criteria := imap.NewSearchCriteria()
		criteria.Or = append(criteria.Or, [2]*imap.SearchCriteria{build(start, middle), build(middle, end)})
		return criteria
	}
	criteria := build(0, len(recipients))
	if days > 0 {
		criteria.Since = now.AddDate(0, 0, -days)
	}
	return criteria
}

func recipientTextSearchCriteria(recipients []string, query, field string, days int, now time.Time) *imap.SearchCriteria {
	criteria := recipientSearchCriteria(recipients, days, now)
	if query == "" {
		return criteria
	}
	switch strings.ToLower(field) {
	case "subject":
		criteria.Header.Add("Subject", query)
	case "from":
		criteria.Header.Add("From", query)
	case "to":
		criteria.Header.Add("To", query)
	case "body":
		criteria.Body = append(criteria.Body, query)
	default:
		criteria.Text = append(criteria.Text, query)
	}
	return criteria
}

func normalizeUIDs(uids []uint32, limit int) []uint32 {
	seen := make(map[uint32]struct{}, len(uids))
	unique := make([]uint32, 0, len(uids))
	for _, uid := range uids {
		if uid == 0 {
			continue
		}
		if _, exists := seen[uid]; exists {
			continue
		}
		seen[uid] = struct{}{}
		unique = append(unique, uid)
	}
	sort.Slice(unique, func(i, j int) bool { return unique[i] < unique[j] })
	return newestUIDs(unique, limit)
}

func envelopeMatchesRecipients(envelope *imap.Envelope, recipients map[string]struct{}) bool {
	if envelope == nil {
		return false
	}
	for _, address := range envelope.To {
		if address == nil {
			continue
		}
		// The mailbox name comes from the parsed IMAP ENVELOPE and may legally
		// contain leading or trailing whitespace when it was a quoted local-part.
		// Trimming here would turn e.g. " alias"@icloud.com into the distinct
		// configured alias alias@icloud.com and cross the HME authorization scope.
		normalized := strings.ToLower(address.Address())
		if _, ok := recipients[normalized]; ok {
			return true
		}
	}
	return false
}

// fetchVerifiedEnvelopeUIDs fetches only envelope metadata, in bounded
// batches, and preserves the normalized ascending UID order. Responses for
// unrequested UIDs and duplicate unsolicited responses are ignored.
func (c *Client) fetchVerifiedEnvelopeUIDs(uids []uint32, recipients map[string]struct{}) ([]uint32, error) {
	if len(uids) == 0 || len(recipients) == 0 {
		return []uint32{}, nil
	}
	verifiedSet := make(map[uint32]struct{}, len(uids))
	for start := 0; start < len(uids); start += searchEnvelopeBatch {
		end := min(start+searchEnvelopeBatch, len(uids))
		batch := uids[start:end]
		requested := make(map[uint32]struct{}, len(batch))
		seqset := new(imap.SeqSet)
		for _, uid := range batch {
			requested[uid] = struct{}{}
			seqset.AddNum(uid)
		}
		messages := make(chan *imap.Message, len(batch))
		done := make(chan error, 1)
		go func() {
			done <- c.cli.UidFetch(seqset, []imap.FetchItem{imap.FetchUid, imap.FetchEnvelope}, messages)
		}()
		seen := make(map[uint32]struct{}, len(batch))
		for msg := range messages {
			if msg == nil || msg.Envelope == nil {
				continue
			}
			if _, ok := requested[msg.Uid]; !ok {
				continue
			}
			if _, duplicate := seen[msg.Uid]; duplicate {
				continue
			}
			seen[msg.Uid] = struct{}{}
			if envelopeMatchesRecipients(msg.Envelope, recipients) {
				verifiedSet[msg.Uid] = struct{}{}
			}
		}
		if err := <-done; err != nil {
			return nil, err
		}
	}
	verified := make([]uint32, 0, len(verifiedSet))
	for _, uid := range uids {
		if _, ok := verifiedSet[uid]; ok {
			verified = append(verified, uid)
		}
	}
	return verified, nil
}

// ForEachByRecipient 按新→旧遍历发给 recipient 的最近 limit 封邮件。
// onMsg 返回 false 时立即停止(用于 OTP 命中即返回)。
func (c *Client) ForEachByRecipient(recipient string, limit int, days int, onMsg func(Message) bool) error {
	if c.cli == nil {
		return fmt.Errorf("未连接")
	}
	if onMsg == nil {
		return fmt.Errorf("onMsg 不能为空")
	}
	if limit <= 0 {
		limit = 5
	}

	if _, err := c.cli.Select("INBOX", true); err != nil {
		return err
	}

	// 1) 服务端按 To 搜索
	criteria := imap.NewSearchCriteria()
	criteria.Header.Add("To", recipient)
	if days > 0 {
		criteria.Since = time.Now().AddDate(0, 0, -days)
	}
	uids, err := c.cli.UidSearch(criteria)
	if err == nil && len(uids) > 0 {
		// UID 升序 → 取最后 limit 个(最新) → 倒序遍历
		uids = newestUIDs(uids, limit)
		for i := len(uids) - 1; i >= 0; i-- {
			m, ferr := c.fetchOneUID(uids[i])
			if ferr != nil {
				return ferr
			}
			if !onMsg(m) {
				return nil
			}
		}
		return nil
	}

	// 2) fallback: 只扫最近 N 封信封, 命中 To 再拉 body
	recipientSet := map[string]struct{}{strings.ToLower(strings.TrimSpace(recipient)): {}}
	return c.forEachRecentMatchingSet(recipientSet, limit, days, onMsg)
}

// newestUIDs 保留 UID 列表中最新的 limit 个(假定 UID 升序)。
func newestUIDs(uids []uint32, limit int) []uint32 {
	if limit <= 0 || len(uids) <= limit {
		return uids
	}
	return uids[len(uids)-limit:]
}

// forEachRecentMatching 拉取收件箱最近 scan 封(仅 envelope), 本地按 To 过滤后再取 body。
func (c *Client) forEachRecentMatching(recipient string, limit int, days int, onMsg func(Message) bool) error {
	return c.forEachRecentMatchingSet(map[string]struct{}{strings.ToLower(strings.TrimSpace(recipient)): {}}, limit, days, onMsg)
}

func (c *Client) forEachRecentMatchingSet(recipients map[string]struct{}, limit int, days int, onMsg func(Message) bool) error {
	mbox, err := c.cli.Select("INBOX", true)
	if err != nil {
		return err
	}
	total := int(mbox.Messages)
	if total == 0 {
		return nil
	}
	// 只扫最近 scan 封, 避免全箱
	scan := limit * 4
	if scan < 20 {
		scan = 20
	}
	if scan > 80 {
		scan = 80
	}
	if scan > total {
		scan = total
	}
	from := mbox.Messages - uint32(scan) + 1
	seqset := new(imap.SeqSet)
	seqset.AddRange(from, mbox.Messages)

	// 仅 envelope + date, 不拉 body
	items := []imap.FetchItem{imap.FetchUid, imap.FetchEnvelope, imap.FetchInternalDate}
	messages := make(chan *imap.Message, scan)
	done := make(chan error, 1)
	go func() {
		done <- c.cli.Fetch(seqset, items, messages)
	}()

	type cand struct {
		uid  uint32
		date time.Time
		to   string
	}
	var cands []cand
	for msg := range messages {
		if msg == nil || msg.Envelope == nil {
			continue
		}
		to := ""
		if len(msg.Envelope.To) > 0 {
			parts := make([]string, 0, len(msg.Envelope.To))
			for _, a := range msg.Envelope.To {
				parts = append(parts, a.Address())
			}
			to = strings.Join(parts, ", ")
		}
		if !envelopeMatchesRecipients(msg.Envelope, recipients) {
			continue
		}
		if days > 0 && !msg.Envelope.Date.IsZero() {
			if time.Since(msg.Envelope.Date) > time.Duration(days)*24*time.Hour {
				continue
			}
		}
		cands = append(cands, cand{uid: msg.Uid, date: msg.Envelope.Date, to: to})
	}
	if err := <-done; err != nil {
		return err
	}
	// 新→旧
	for i := 0; i < len(cands); i++ {
		for j := i + 1; j < len(cands); j++ {
			if cands[j].date.After(cands[i].date) {
				cands[i], cands[j] = cands[j], cands[i]
			}
		}
	}
	n := 0
	for _, cd := range cands {
		if n >= limit {
			break
		}
		m, ferr := c.fetchOneUID(cd.uid)
		if ferr != nil {
			return ferr
		}
		n++
		if !onMsg(m) {
			return nil
		}
	}
	return nil
}

// fetchOneUID 拉取单封邮件(含 body preview), 使用 BODY.PEEK 不标已读。
func (c *Client) fetchOneUID(uid uint32) (Message, error) {
	msg, err := c.fetchOneUIDRaw(uid)
	if err != nil {
		return Message{}, err
	}
	return toMessageWithBody(msg), nil
}

func (c *Client) fetchOneUIDRaw(uid uint32) (*imap.Message, error) {
	seqset := new(imap.SeqSet)
	seqset.AddNum(uid)
	section := &imap.BodySectionName{Peek: true}
	items := []imap.FetchItem{imap.FetchUid, imap.FetchEnvelope, imap.FetchInternalDate, section.FetchItem()}
	messages := make(chan *imap.Message, 1)
	done := make(chan error, 1)
	go func() {
		done <- c.cli.UidFetch(seqset, items, messages)
	}()
	msg, err := drainUIDFetch(uid, messages, done, hasEnvelopeAndBody)
	if err != nil {
		return nil, err
	}
	return msg, nil
}

func (c *Client) fetchByUIDs(uids []uint32, limit int) ([]Message, error) {
	if len(uids) == 0 {
		return []Message{}, nil
	}
	uids = newestUIDs(uids, limit)
	var out []Message
	// 新→旧
	for i := len(uids) - 1; i >= 0; i-- {
		m, err := c.fetchOneUID(uids[i])
		if err != nil {
			return out, err
		}
		out = append(out, m)
	}
	return out, nil
}

// fetchMatchingUIDs rechecks IMAP SEARCH candidates against the parsed
// envelope. HEADER searches are substring-based on some servers, so the
// search result alone is not a safe HME scope boundary.
func (c *Client) fetchMatchingUIDs(uids []uint32, recipients map[string]struct{}, limit int) ([]Message, error) {
	if len(uids) == 0 || len(recipients) == 0 {
		return []Message{}, nil
	}
	out := make([]Message, 0, min(limit, len(uids)))
	for i := len(uids) - 1; i >= 0; i-- {
		raw, err := c.fetchOneUIDRaw(uids[i])
		if err != nil {
			return out, err
		}
		if !envelopeMatchesRecipients(raw.Envelope, recipients) {
			continue
		}
		out = append(out, toMessageWithBody(raw))
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

// GetFull 获取单封邮件的完整内容(含正文)。
func (c *Client) GetFull(uid uint32) (*FullMessage, error) {
	return c.getFull(uid, nil)
}

func (c *Client) getFull(uid uint32, recipients map[string]struct{}) (*FullMessage, error) {
	if c.cli == nil {
		return nil, fmt.Errorf("未连接")
	}
	if _, err := c.cli.Select("INBOX", true); err != nil {
		return nil, err
	}

	seqset := new(imap.SeqSet)
	seqset.AddNum(uid)

	items := []imap.FetchItem{imap.FetchUid, imap.FetchEnvelope, imap.FetchInternalDate, imap.FetchRFC822}
	messages := make(chan *imap.Message, 1)
	done := make(chan error, 1)
	go func() {
		done <- c.cli.UidFetch(seqset, items, messages)
	}()
	msg, err := drainUIDFetch(uid, messages, done, hasEnvelopeAndBody)
	if err != nil {
		return nil, err
	}
	if recipients != nil && !envelopeMatchesRecipients(msg.Envelope, recipients) {
		return nil, ErrMessageOutsideHME
	}

	r := msg.GetBody(&imap.BodySectionName{})
	if r == nil {
		return nil, fmt.Errorf("邮件正文为空 (uid=%d)", uid)
	}
	parsed, err := parseRFC822(r)
	if err != nil {
		return nil, fmt.Errorf("解析邮件正文失败 (uid=%d): %w", uid, err)
	}
	full := &FullMessage{
		Message:       toMessage(msg),
		Body:          parsed.body,
		HTMLBody:      parsed.htmlBody,
		BodyTruncated: parsed.truncated,
		ContentType:   parsed.contentType,
	}
	return full, nil
}

// GetFullForRecipients returns a full message only when its parsed envelope is
// addressed to one of the verified HME aliases.
func (c *Client) GetFullForRecipients(uid uint32, recipients []string) (*FullMessage, error) {
	set := make(map[string]struct{})
	for _, recipient := range normalizeRecipients(recipients) {
		set[recipient] = struct{}{}
	}
	if len(set) == 0 {
		return nil, ErrMessageOutsideHME
	}
	return c.getFull(uid, set)
}

// drainUIDFetch consumes the whole message stream before waiting for UidFetch's
// result. The go-imap response handler sends synchronously to this channel, so
// reading only one response can deadlock it if a server emits duplicate FETCH
// responses before the tagged completion. Keep the first usable response for
// the requested UID and discard the rest without allowing a later duplicate to
// overwrite it.
func drainUIDFetch(
	uid uint32,
	messages <-chan *imap.Message,
	done <-chan error,
	usable func(*imap.Message) bool,
) (*imap.Message, error) {
	var selected *imap.Message
	matchedUID := false
	for msg := range messages {
		if msg == nil || msg.Uid != uid {
			continue
		}
		matchedUID = true
		if selected != nil {
			continue
		}
		if usable != nil && !usable(msg) {
			continue
		}
		selected = msg
	}
	if err := <-done; err != nil {
		return nil, err
	}
	if selected == nil {
		if matchedUID {
			return nil, fmt.Errorf("%w: 邮件内容不完整 (uid=%d)", ErrMessageOutsideHME, uid)
		}
		return nil, fmt.Errorf("%w (uid=%d)", ErrMessageOutsideHME, uid)
	}
	return selected, nil
}

func hasEnvelopeAndBody(msg *imap.Message) bool {
	return msg.Envelope != nil && msg.GetBody(&imap.BodySectionName{}) != nil
}

func hasEnvelope(msg *imap.Message) bool {
	return msg.Envelope != nil
}

// Delete 删除收件箱中指定 UID 的邮件。
func (c *Client) Delete(uid uint32) error {
	if c.cli == nil {
		return fmt.Errorf("未连接")
	}
	if uid == 0 {
		return fmt.Errorf("邮件 UID 无效")
	}
	if _, err := c.cli.Select("INBOX", false); err != nil {
		return err
	}
	return c.deleteSelectedUID(uid)
}

// DeleteForRecipients verifies the target envelope and deletes it without
// reselecting the mailbox, keeping validation and mutation in one IMAP session.
func (c *Client) DeleteForRecipients(uid uint32, recipients []string) error {
	if uid == 0 {
		return ErrMessageOutsideHME
	}
	set := make(map[string]struct{})
	for _, recipient := range normalizeRecipients(recipients) {
		set[recipient] = struct{}{}
	}
	if len(set) == 0 {
		return ErrMessageOutsideHME
	}
	if c.cli == nil {
		return fmt.Errorf("未连接")
	}
	if _, err := c.cli.Select("INBOX", false); err != nil {
		return err
	}

	seqset := new(imap.SeqSet)
	seqset.AddNum(uid)
	messages := make(chan *imap.Message, 1)
	done := make(chan error, 1)
	go func() {
		done <- c.cli.UidFetch(seqset, []imap.FetchItem{imap.FetchUid, imap.FetchEnvelope}, messages)
	}()
	msg, err := drainUIDFetch(uid, messages, done, hasEnvelope)
	if err != nil {
		return err
	}
	if !envelopeMatchesRecipients(msg.Envelope, set) {
		return ErrMessageOutsideHME
	}
	return c.deleteSelectedUID(uid)
}

func (c *Client) deleteSelectedUID(uid uint32) error {
	seqset := new(imap.SeqSet)
	seqset.AddNum(uid)
	uidPlusClient := uidplus.NewClient(c.cli)
	supported, err := uidPlusClient.SupportUidPlus()
	if err != nil {
		return err
	}
	if !supported {
		return fmt.Errorf("邮件服务器不支持安全删除 (UIDPLUS)")
	}
	item := imap.FormatFlagsOp(imap.AddFlags, true)
	if err := c.cli.UidStore(seqset, item, []interface{}{imap.DeletedFlag}, nil); err != nil {
		return err
	}
	return uidPlusClient.UidExpunge(seqset, nil)
}

// ---- 解析工具 ----

func toMessage(msg *imap.Message) Message {
	m := Message{}
	if msg.Uid > 0 {
		m.ID = fmt.Sprintf("%d", msg.Uid)
	}
	if msg.Envelope != nil {
		if len(msg.Envelope.From) > 0 {
			m.From = msg.Envelope.From[0].Address()
		}
		if len(msg.Envelope.To) > 0 {
			addrs := make([]string, 0, len(msg.Envelope.To))
			for _, a := range msg.Envelope.To {
				addrs = append(addrs, a.Address())
			}
			m.To = strings.Join(addrs, ", ")
		}
		m.Subject = decodeHeader(msg.Envelope.Subject)
		if !msg.Envelope.Date.IsZero() {
			m.Date = msg.Envelope.Date.Format(time.RFC3339)
		}
	}
	if m.From != "" {
		m.From = decodeHeader(m.From)
	}
	if m.To != "" {
		m.To = decodeHeader(m.To)
	}
	return m
}

// toMessageWithBody 在 toMessage 基础上解析正文填充 Preview(供 OTP 提取)。
func toMessageWithBody(msg *imap.Message) Message {
	m := toMessage(msg)
	// Fetch 可能用 BODY[] 或 BODY.PEEK[], 两种 section 都试
	for _, section := range []*imap.BodySectionName{{Peek: true}, {}} {
		r := msg.GetBody(section)
		if r == nil {
			continue
		}
		parsed, err := parseRFC822(r)
		if err != nil {
			continue
		}
		m.Preview = truncateRunes(strings.TrimSpace(parsed.body), previewRuneLimit)
		break
	}
	return m
}

// decodeHeader 解码 RFC 2047 编码的邮件头(如 =?UTF-8?B?xxx?=)。
func decodeHeader(s string) string {
	if s == "" {
		return ""
	}
	dec := mime.WordDecoder{CharsetReader: charset.Reader}
	out, err := dec.DecodeHeader(s)
	if err != nil {
		return s
	}
	return out
}
