package mail

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"strings"

	message "github.com/emersion/go-message"
)

const (
	maxMIMEDepth      = 16
	maxMIMENodes      = 128
	maxMIMEHeaderSize = 64 << 10
	maxPlainBodySize  = 1 << 20
	maxHTMLBodySize   = 2 << 20
	previewRuneLimit  = 1000
)

type parsedMessage struct {
	body        string
	htmlBody    string
	contentType string
	truncated   bool
}

type mimeParser struct {
	result parsedMessage
	plain  string
	nodes  int
}

// parseRFC822 decodes a complete RFC 822 message. MIME containers are walked
// explicitly so depth, total node count, and attachment inheritance can be
// enforced before a subtree is expanded.
func parseRFC822(r io.Reader) (parsedMessage, error) {
	entity, err := message.ReadWithOptions(r, &message.ReadOptions{MaxHeaderBytes: maxMIMEHeaderSize})
	if err != nil && !message.IsUnknownCharset(err) {
		return parsedMessage{}, err
	}
	if entity == nil {
		return parsedMessage{}, fmt.Errorf("MIME entity is unavailable")
	}

	parser := mimeParser{
		result: parsedMessage{contentType: entity.Header.Get("Content-Type")},
	}
	if err := parser.visit(entity, 1, false); err != nil {
		return parsedMessage{}, err
	}
	if parser.plain != "" {
		parser.result.body = parser.plain
	} else if parser.result.htmlBody != "" {
		parser.result.body = sanitizePreview(parser.result.htmlBody)
	}
	return parser.result, nil
}

func (p *mimeParser) visit(entity *message.Entity, depth int, parentAttachment bool) error {
	if depth > maxMIMEDepth {
		return fmt.Errorf("MIME depth exceeds %d", maxMIMEDepth)
	}
	p.nodes++
	if p.nodes > maxMIMENodes {
		return fmt.Errorf("MIME node count exceeds %d", maxMIMENodes)
	}

	mediaType, typeParams, err := entity.Header.ContentType()
	if err != nil {
		if depth == 1 {
			return fmt.Errorf("invalid root MIME Content-Type: %w", err)
		}
		return nil
	}
	mediaType = strings.ToLower(mediaType)

	disposition, dispositionParams, _ := entity.Header.ContentDisposition()
	isAttachment := parentAttachment || strings.EqualFold(disposition, "attachment") ||
		dispositionParams["filename"] != "" || typeParams["name"] != ""
	if isAttachment {
		// Do not create a MultipartReader: the parent will discard this raw part
		// when advancing, so an attached container's descendants are never parsed.
		return nil
	}

	if strings.HasPrefix(mediaType, "multipart/") {
		boundary := typeParams["boundary"]
		if err := validateMIMEBoundary(boundary); err != nil {
			return err
		}

		// go-message limits the top-level header, but its MultipartReader does
		// not expose a per-part header limit. This streaming wrapper rejects an
		// oversized child header before the library can keep expanding it.
		entity.Body = newMultipartHeaderLimitReader(entity.Body, boundary)
		mr := entity.MultipartReader()
		if mr == nil {
			return fmt.Errorf("MIME multipart reader is unavailable")
		}
		defer mr.Close()

		for {
			part, nextErr := mr.NextPart()
			if nextErr == io.EOF {
				return nil
			}
			if nextErr != nil && !message.IsUnknownCharset(nextErr) {
				return nextErr
			}
			if part == nil {
				return fmt.Errorf("MIME part is unavailable")
			}
			if err := p.visit(part, depth+1, false); err != nil {
				return err
			}
		}
	}

	if mediaType != "text/plain" && mediaType != "text/html" {
		return nil
	}
	if mediaType == "text/plain" && p.plain != "" {
		return nil
	}
	if mediaType == "text/html" && p.result.htmlBody != "" {
		return nil
	}

	limit := int64(maxPlainBodySize)
	if mediaType == "text/html" {
		limit = maxHTMLBodySize
	}
	data, readErr := io.ReadAll(io.LimitReader(entity.Body, limit+1))
	if readErr != nil {
		return readErr
	}
	wasTruncated := int64(len(data)) > limit
	if wasTruncated {
		data = data[:limit]
	}

	if mediaType == "text/plain" {
		p.plain = sanitizePlainPreview(string(data))
	} else {
		p.result.htmlBody = string(data)
	}
	p.result.truncated = p.result.truncated || wasTruncated
	return nil
}

// validateMIMEBoundary applies the RFC 2046 boundary grammar to the decoded
// parameter returned by Header.ContentType. A boundary is 1-70 ASCII bchars,
// and its final byte must be bcharsnospace.
func validateMIMEBoundary(boundary string) error {
	if len(boundary) == 0 || len(boundary) > 70 {
		return fmt.Errorf("invalid MIME multipart boundary")
	}
	for i := 0; i < len(boundary); i++ {
		b := boundary[i]
		if isMIMEBoundaryNoSpace(b) || b == ' ' && i < len(boundary)-1 {
			continue
		}
		return fmt.Errorf("invalid MIME multipart boundary")
	}
	return nil
}

func isMIMEBoundaryNoSpace(b byte) bool {
	if b >= '0' && b <= '9' || b >= 'A' && b <= 'Z' || b >= 'a' && b <= 'z' {
		return true
	}
	switch b {
	case '\'', '(', ')', '+', '_', ',', '-', '.', '/', ':', '=', '?':
		return true
	default:
		return false
	}
}

// multipartHeaderLimitReader observes one multipart level. It passes the body
// through unchanged while counting bytes only between this level's boundary
// delimiter and the blank line that ends the following child header.
type multipartHeaderLimitReader struct {
	reader         *bufio.Reader
	boundary       []byte
	inHeader       bool
	headerBytes    int
	fragmentedLine bool
	pending        []byte
	pendingErr     error
}

func newMultipartHeaderLimitReader(r io.Reader, boundary string) io.Reader {
	return &multipartHeaderLimitReader{
		reader:   bufio.NewReader(r),
		boundary: []byte("--" + boundary),
	}
}

func (r *multipartHeaderLimitReader) Read(p []byte) (int, error) {
	if len(r.pending) == 0 {
		if r.pendingErr != nil {
			err := r.pendingErr
			r.pendingErr = nil
			return 0, err
		}
		if err := r.readChunk(); err != nil {
			return 0, err
		}
	}

	n := copy(p, r.pending)
	r.pending = r.pending[n:]
	if len(r.pending) == 0 && r.pendingErr != nil {
		err := r.pendingErr
		r.pendingErr = nil
		return n, err
	}
	return n, nil
}

func (r *multipartHeaderLimitReader) readChunk() error {
	chunk, readErr := r.reader.ReadSlice('\n')
	fragmented := readErr == bufio.ErrBufferFull
	if fragmented {
		readErr = nil
	}
	if len(chunk) == 0 {
		return readErr
	}

	if r.inHeader {
		remaining := maxMIMEHeaderSize - r.headerBytes
		if len(chunk) > remaining {
			if remaining > 0 {
				r.pending = append(r.pending[:0], chunk[:remaining]...)
				r.headerBytes += remaining
				r.pendingErr = fmt.Errorf("MIME part header exceeds %d bytes", maxMIMEHeaderSize)
				return nil
			}
			return fmt.Errorf("MIME part header exceeds %d bytes", maxMIMEHeaderSize)
		}
		r.headerBytes += len(chunk)
		if !r.fragmentedLine && !fragmented && isBlankMIMELine(chunk) {
			r.inHeader = false
			r.headerBytes = 0
		}
	} else if !r.fragmentedLine && !fragmented {
		switch classifyMIMEBoundaryLine(chunk, r.boundary) {
		case 1:
			r.inHeader = true
			r.headerBytes = 0
		case 2:
			r.inHeader = false
		}
	}

	if fragmented {
		r.fragmentedLine = true
	} else {
		r.fragmentedLine = false
	}
	r.pending = append(r.pending[:0], chunk...)
	r.pendingErr = readErr
	return nil
}

func isBlankMIMELine(line []byte) bool {
	return bytes.Equal(line, []byte("\n")) || bytes.Equal(line, []byte("\r\n"))
}

func classifyMIMEBoundaryLine(line, boundary []byte) int {
	hasLineEnding := bytes.HasSuffix(line, []byte("\n"))
	if hasLineEnding {
		line = bytes.TrimSuffix(line, []byte("\n"))
		line = bytes.TrimSuffix(line, []byte("\r"))
	}
	if !bytes.HasPrefix(line, boundary) {
		return 0
	}

	rest := line[len(boundary):]
	if bytes.HasPrefix(rest, []byte("--")) {
		rest = rest[2:]
		if isMIMETransportPadding(rest) {
			return 2
		}
		return 0
	}
	if hasLineEnding && isMIMETransportPadding(rest) {
		return 1
	}
	return 0
}

func isMIMETransportPadding(value []byte) bool {
	for _, b := range value {
		if b != ' ' && b != '\t' {
			return false
		}
	}
	return true
}

func truncateRunes(value string, limit int) string {
	if limit < 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "…"
}
