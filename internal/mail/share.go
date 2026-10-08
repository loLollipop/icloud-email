package mail

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/emersion/go-imap"
)

// ErrShareIdentityChanged invalidates the capability rather than widening it.
var ErrShareIdentityChanged = errors.New("shared mailbox identity changed")

// ShareBoundary binds a grant to the selected mailbox and its next unused UID.
// ConfigurationVersion is checked by the account adapter while holding its lock.
type ShareBoundary struct {
	MailboxIdentity      string    `json:"mailbox_identity"`
	ConfigurationVersion string    `json:"configuration_version"`
	UIDValidity          uint32    `json:"uid_validity"`
	MinUID               uint32    `json:"min_uid"`
	CreatedAt            time.Time `json:"created_at"`
}

func (c *Client) shareIdentity() string {
	// Never persist a credential, even as part of the otherwise non-secret identity.
	raw, _ := json.Marshal([]any{c.username, c.server, c.port, "INBOX", c.password})
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func (c *Client) CaptureShareBoundary() (ShareBoundary, error) {
	if c.cli == nil {
		return ShareBoundary{}, fmt.Errorf("未连接")
	}
	mbox, err := c.cli.Select("INBOX", true)
	if err != nil {
		return ShareBoundary{}, err
	}
	if mbox.UidValidity == 0 || mbox.UidNext == 0 {
		return ShareBoundary{}, ErrShareIdentityChanged
	}
	return ShareBoundary{MailboxIdentity: c.shareIdentity(), UIDValidity: mbox.UidValidity, MinUID: mbox.UidNext}, nil
}

func (c *Client) selectShared(boundary ShareBoundary) error {
	if c.cli == nil {
		return fmt.Errorf("未连接")
	}
	if boundary.MailboxIdentity == "" || boundary.MailboxIdentity != c.shareIdentity() || boundary.UIDValidity == 0 || boundary.MinUID == 0 || boundary.CreatedAt.IsZero() {
		return ErrShareIdentityChanged
	}
	mbox, err := c.cli.Select("INBOX", true)
	if err != nil {
		return err
	}
	if mbox.UidValidity != boundary.UIDValidity || mbox.UidNext < boundary.MinUID {
		return ErrShareIdentityChanged
	}
	return nil
}

func (c *Client) ValidateShareBoundary(boundary ShareBoundary) error { return c.selectShared(boundary) }

func sharedMessageAllowed(msg *imap.Message, boundary ShareBoundary, recipient string) bool {
	return msg != nil && msg.Uid >= boundary.MinUID && !msg.InternalDate.IsZero() && msg.InternalDate.After(boundary.CreatedAt) &&
		envelopeMatchesRecipients(msg.Envelope, map[string]struct{}{strings.ToLower(strings.TrimSpace(recipient)): {}})
}

// SearchShared verifies all candidate metadata BEFORE computing count or pages.
// IMAP SINCE has day precision and UID n:* may return an older highest UID, so
// neither SEARCH predicate is treated as the authorization decision.
func (c *Client) SearchShared(boundary ShareBoundary, recipient string, page, pageSize int, query string) (SearchResult, error) {
	if page < 1 || pageSize < 1 || pageSize > 100 || len([]rune(query)) > 256 {
		return SearchResult{}, fmt.Errorf("invalid shared query")
	}
	if err := c.selectShared(boundary); err != nil {
		return SearchResult{}, err
	}
	criteria := recipientTextSearchCriteria([]string{recipient}, strings.TrimSpace(query), "subject", 0, time.Now())
	criteria.Uid = new(imap.SeqSet)
	criteria.Uid.AddRange(boundary.MinUID, 0)
	criteria.Since = boundary.CreatedAt
	uids, err := c.cli.UidSearch(criteria)
	if err != nil {
		return SearchResult{}, err
	}
	uids = normalizeUIDs(uids, 0)
	verified := make([]uint32, 0, len(uids))
	for start := 0; start < len(uids); start += searchEnvelopeBatch {
		batch := uids[start:min(start+searchEnvelopeBatch, len(uids))]
		set := new(imap.SeqSet)
		requested := make(map[uint32]bool, len(batch))
		for _, uid := range batch {
			if uid >= boundary.MinUID {
				set.AddNum(uid)
				requested[uid] = true
			}
		}
		if len(requested) == 0 {
			continue
		}
		responses := make(chan *imap.Message, len(batch))
		done := make(chan error, 1)
		go func() {
			done <- c.cli.UidFetch(set, []imap.FetchItem{imap.FetchUid, imap.FetchEnvelope, imap.FetchInternalDate}, responses)
		}()
		allowed := make(map[uint32]bool, len(batch))
		for msg := range responses {
			if msg == nil || !requested[msg.Uid] {
				continue
			}
			delete(requested, msg.Uid)
			if sharedMessageAllowed(msg, boundary, recipient) && (query == "" || strings.Contains(strings.ToLower(decodeHeader(msg.Envelope.Subject)), strings.ToLower(strings.TrimSpace(query)))) {
				allowed[msg.Uid] = true
			}
		}
		if err := <-done; err != nil {
			return SearchResult{}, err
		}
		for _, uid := range batch {
			if allowed[uid] {
				verified = append(verified, uid)
			}
		}
	}
	total := len(verified)
	pages := max(1, (total+pageSize-1)/pageSize)
	page = min(page, pages)
	start, end := (page-1)*pageSize, min(page*pageSize, total)
	result := SearchResult{Messages: []Message{}, Total: total, Page: page, PageSize: pageSize}
	pageUIDs := make([]uint32, 0, end-start)
	for i := start; i < end; i++ {
		pageUIDs = append(pageUIDs, verified[total-1-i])
	}
	result.Messages, err = c.fetchSharedPreviews(pageUIDs, boundary, recipient)
	if err != nil {
		return SearchResult{}, err
	}
	if err := c.selectShared(boundary); err != nil {
		return SearchResult{}, err
	}
	return result, nil
}

// One bounded PEEK FETCH downloads the page; every response is scoped again.
func (c *Client) fetchSharedPreviews(uids []uint32, boundary ShareBoundary, recipient string) ([]Message, error) {
	if len(uids) == 0 {
		return []Message{}, nil
	}
	set := new(imap.SeqSet)
	requested := make(map[uint32]bool, len(uids))
	for _, uid := range uids {
		set.AddNum(uid)
		requested[uid] = true
	}
	section := &imap.BodySectionName{Peek: true, Partial: []int{0, searchPreviewBytes}}
	responses := make(chan *imap.Message, len(uids))
	done := make(chan error, 1)
	go func() {
		done <- c.cli.UidFetch(set, []imap.FetchItem{imap.FetchUid, imap.FetchEnvelope, imap.FetchInternalDate, section.FetchItem()}, responses)
	}()
	selected := make(map[uint32]*imap.Message, len(uids))
	for msg := range responses {
		if msg == nil || !requested[msg.Uid] || msg.Envelope == nil || msg.GetBody(section) == nil {
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
		if !sharedMessageAllowed(raw, boundary, recipient) {
			return nil, ErrMessageOutsideHME
		}
		message := toMessage(raw)
		body := raw.GetBody(section)
		if parsed, err := parseRFC822Preview(body, body.Len() >= searchPreviewBytes); err == nil {
			message.Preview = truncateRunes(strings.TrimSpace(parsed.body), previewRuneLimit)
		}
		message.Date = raw.InternalDate.UTC().Format(time.RFC3339)
		result = append(result, message)
	}
	return result, nil
}

// GetSharedFull deliberately does not call the administrator full-message path,
// which fetches RFC822 and would select the mailbox again without this boundary.
// The wire budget allows encoded plain/HTML content up to the existing MIME
// limits, but bounds attachments and marks a partial MIME message as truncated.
const maxSharedMessageBytes = 2*(maxPlainBodySize+maxHTMLBodySize) + maxMIMEHeaderSize

func (c *Client) fetchSharedUID(uid uint32, section *imap.BodySectionName) (*imap.Message, error) {
	set := new(imap.SeqSet)
	set.AddNum(uid)
	items := []imap.FetchItem{imap.FetchUid, imap.FetchEnvelope, imap.FetchInternalDate}
	if section != nil {
		items = append(items, section.FetchItem())
	}
	responses, done := make(chan *imap.Message, 1), make(chan error, 1)
	go func() { done <- c.cli.UidFetch(set, items, responses) }()
	return drainUIDFetch(uid, responses, done, func(msg *imap.Message) bool {
		return msg.Envelope != nil && (section == nil || msg.GetBody(section) != nil)
	})
}

func (c *Client) GetSharedFull(boundary ShareBoundary, recipient string, uid uint32) (*FullMessage, error) {
	if err := c.selectShared(boundary); err != nil {
		return nil, err
	}
	if uid < boundary.MinUID {
		return nil, ErrMessageOutsideHME
	}
	// Authorization only fetches metadata; guessing another alias's UID cannot
	// trigger its body/attachment download.
	metadata, err := c.fetchSharedUID(uid, nil)
	if err != nil {
		return nil, err
	}
	if !sharedMessageAllowed(metadata, boundary, recipient) {
		return nil, ErrMessageOutsideHME
	}
	if err := c.selectShared(boundary); err != nil {
		return nil, err
	}
	section := &imap.BodySectionName{Peek: true, Partial: []int{0, maxSharedMessageBytes}}
	raw, err := c.fetchSharedUID(uid, section)
	if err != nil {
		return nil, err
	}
	if !sharedMessageAllowed(raw, boundary, recipient) || !raw.InternalDate.Equal(metadata.InternalDate) {
		return nil, ErrMessageOutsideHME
	}
	if err := c.selectShared(boundary); err != nil {
		return nil, err
	}
	body := raw.GetBody(section)
	if body.Len() > maxSharedMessageBytes {
		return nil, fmt.Errorf("shared body exceeded requested FETCH limit")
	}
	parsed, err := parseRFC822Message(body, body.Len() == maxSharedMessageBytes)
	if err != nil {
		return nil, err
	}
	message := toMessage(raw)
	message.Date = raw.InternalDate.UTC().Format(time.RFC3339)
	return &FullMessage{Message: message, Body: parsed.body, HTMLBody: parsed.htmlBody, BodyTruncated: parsed.truncated, ContentType: parsed.contentType}, nil
}
