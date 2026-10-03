package mail

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"io"
	"strings"
	"testing"
)

func TestParseRFC822MultipartAlternative(t *testing.T) {
	html := `<html><body><a href="https://goodstack.example/status" style="background:#1677ff;color:white">Check status</a></body></html>`
	raw := "MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/alternative; boundary=alt\r\n\r\n" +
		"--alt\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\nPlain=20body\r\n" +
		"--alt\r\nContent-Type: text/html; charset=utf-8\r\nContent-Transfer-Encoding: base64\r\n\r\n" + base64.StdEncoding.EncodeToString([]byte(html)) + "\r\n" +
		"--alt--\r\n"

	got, err := parseRFC822(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("parseRFC822: %v", err)
	}
	if got.body != "Plain body" {
		t.Fatalf("body = %q, want plain alternative", got.body)
	}
	if got.htmlBody != html {
		t.Fatalf("htmlBody did not preserve Goodstack link:\n%s", got.htmlBody)
	}
	if !strings.Contains(got.htmlBody, `<a href="https://goodstack.example/status"`) || !strings.Contains(got.htmlBody, `>Check status</a>`) {
		t.Fatalf("Goodstack-style anchor was not preserved: %s", got.htmlBody)
	}
	if got.contentType != "multipart/alternative; boundary=alt" {
		t.Fatalf("contentType = %q", got.contentType)
	}
}

func TestParseRFC822AlternativeOrderAlwaysPrefersPlain(t *testing.T) {
	parts := map[string]string{
		"plain": "Content-Type: text/plain; charset=utf-8\r\n\r\nPreferred plain\r\n",
		"html":  "Content-Type: text/html; charset=utf-8\r\n\r\n<p>HTML fallback</p>\r\n",
	}
	for _, order := range [][]string{{"plain", "html"}, {"html", "plain"}} {
		t.Run(strings.Join(order, "-then-"), func(t *testing.T) {
			raw := "Content-Type: multipart/alternative; boundary=x\r\n\r\n"
			for _, kind := range order {
				raw += "--x\r\n" + parts[kind]
			}
			raw += "--x--\r\n"
			got, err := parseRFC822(strings.NewReader(raw))
			if err != nil {
				t.Fatal(err)
			}
			if got.body != "Preferred plain" || !strings.Contains(got.htmlBody, "HTML fallback") {
				t.Fatalf("unexpected alternatives: %#v", got)
			}
		})
	}
}

func TestParseRFC822NestedRelatedMixedSkipsTextAttachments(t *testing.T) {
	raw := "Content-Type: multipart/mixed; boundary=mix\r\n\r\n" +
		"--mix\r\nContent-Type: multipart/related; boundary=rel\r\n\r\n" +
		"--rel\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<p>Real message</p>\r\n" +
		"--rel\r\nContent-Type: image/png\r\nContent-Disposition: inline; filename=logo.png\r\n\r\nPNG\r\n--rel--\r\n" +
		"--mix\r\nContent-Type: text/plain; name=overwrite.txt\r\nContent-Disposition: attachment; filename=overwrite.txt\r\n\r\nATTACHMENT MUST NOT WIN\r\n" +
		"--mix\r\nContent-Type: text/html; name=other.html\r\nContent-Disposition: inline; filename=other.html\r\n\r\n<p>ATTACHMENT HTML</p>\r\n" +
		"--mix--\r\n"

	got, err := parseRFC822(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if got.body != "Real message" || !strings.Contains(got.htmlBody, "Real message") {
		t.Fatalf("attachment replaced message body: %#v", got)
	}
}

func TestParseRFC822HTMLOnlyCreatesPlainFallback(t *testing.T) {
	raw := "Content-Type: text/html; charset=utf-8\r\n\r\n" +
		"<html><head><style>.x{color:red}</style></head><body><p>Hello &amp; goodbye</p><script>alert(1)</script></body></html>"
	got, err := parseRFC822(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if got.body != "Hello & goodbye" {
		t.Fatalf("fallback body = %q", got.body)
	}
	if !strings.Contains(got.htmlBody, "<p>Hello &amp; goodbye</p>") {
		t.Fatalf("HTML body missing: %q", got.htmlBody)
	}
}

func TestParseRFC822LimitsBodyAndPreview(t *testing.T) {
	raw := "Content-Type: text/plain; charset=utf-8\r\n\r\n" + strings.Repeat("a", maxPlainBodySize+64)
	got, err := parseRFC822(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if !got.truncated || len(got.body) != maxPlainBodySize {
		t.Fatalf("body limit not applied: len=%d truncated=%v", len(got.body), got.truncated)
	}
	preview := truncateRunes(got.body, previewRuneLimit)
	if len([]rune(preview)) != previewRuneLimit+1 || !strings.HasSuffix(preview, "…") {
		t.Fatalf("preview was not rune-truncated: len=%d", len([]rune(preview)))
	}
}

func TestParseRFC822EmptyAndMalformed(t *testing.T) {
	empty, err := parseRFC822(strings.NewReader("Content-Type: text/plain\r\n\r\n"))
	if err != nil || empty.body != "" || empty.htmlBody != "" {
		t.Fatalf("empty body: result=%#v err=%v", empty, err)
	}

	malformed := "Content-Type: multipart/mixed; boundary=missing\r\n\r\n--different\r\n<script>alert(1)</script>"
	if _, err := parseRFC822(strings.NewReader(malformed)); err == nil {
		t.Fatal("expected malformed multipart error")
	}
}

func TestParseRFC822RejectsMalformedRootContentType(t *testing.T) {
	raw := "Content-Type: text/plain; charset\r\n\r\nbody"

	if _, err := parseRFC822(strings.NewReader(raw)); err == nil || !strings.Contains(err.Error(), "Content-Type") {
		t.Fatalf("expected root Content-Type error, got %v", err)
	}
}

func TestParseRFC822SkipsMalformedChildContentType(t *testing.T) {
	raw := "Content-Type: multipart/mixed; boundary=x\r\n\r\n" +
		"--x\r\nContent-Type: text/plain; charset\r\n\r\nBroken child\r\n" +
		"--x\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nGood child\r\n" +
		"--x--\r\n"

	got, err := parseRFC822(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("parseRFC822: %v", err)
	}
	if got.body != "Good child" {
		t.Fatalf("body = %q, want valid child body", got.body)
	}
}

func TestParseRFC822RejectsDepth17(t *testing.T) {
	body := "Content-Type: text/plain; charset=utf-8\r\n\r\ntoo deep\r\n"
	for depth := 16; depth >= 1; depth-- {
		boundary := fmt.Sprintf("depth-%d", depth)
		body = "Content-Type: multipart/mixed; boundary=" + boundary + "\r\n\r\n" +
			"--" + boundary + "\r\n" + body + "--" + boundary + "--\r\n"
	}

	if _, err := parseRFC822(strings.NewReader(body)); err == nil || !strings.Contains(err.Error(), "depth exceeds") {
		t.Fatalf("expected MIME depth error, got %v", err)
	}
}

func TestParseRFC822RejectsNode129(t *testing.T) {
	var raw strings.Builder
	raw.WriteString("Content-Type: multipart/mixed; boundary=many\r\n\r\n")
	for i := 0; i < maxMIMENodes; i++ {
		raw.WriteString("--many\r\nContent-Type: application/octet-stream\r\n\r\nx\r\n")
	}
	raw.WriteString("--many--\r\n")

	if _, err := parseRFC822(strings.NewReader(raw.String())); err == nil || !strings.Contains(err.Error(), "node count exceeds") {
		t.Fatalf("expected MIME node count error, got %v", err)
	}
}

func TestParseRFC822RejectsOversizedChildHeader(t *testing.T) {
	raw := "Content-Type: multipart/mixed; boundary=x\r\n\r\n" +
		"--x\r\nX-Oversized: " + strings.Repeat("a", maxMIMEHeaderSize) + "\r\n\r\nbody\r\n" +
		"--x--\r\n"

	if _, err := parseRFC822(strings.NewReader(raw)); err == nil || !strings.Contains(err.Error(), "header exceeds") {
		t.Fatalf("expected MIME part header error, got %v", err)
	}
}

func TestParseRFC822BoundaryVariantsEnforceChildHeaderLimit(t *testing.T) {
	const contentType = `multipart/mixed; boundary="=?UTF-8?Q?encoded=2Dboundary?="`
	const boundary = "encoded-boundary"
	raw := "Content-Type: " + contentType + "\r\n\r\n" +
		"--" + boundary + "\t \r\nX-Oversized: " + strings.Repeat("a", maxMIMEHeaderSize) + "\r\n\r\nbody\r\n" +
		"--" + boundary + "--\t \r\n"

	if _, err := parseRFC822(strings.NewReader(raw)); err == nil || !strings.Contains(err.Error(), "header exceeds") {
		t.Fatalf("expected MIME part header error, got %v", err)
	}
}

func TestParseRFC822RejectsNonStandardBoundaryVariants(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
	}{
		{
			name:        "boundary ending in space",
			contentType: `multipart/mixed; boundary="space-boundary "`,
		},
		{
			name:        "boundary ending in tab",
			contentType: "multipart/mixed; boundary=\"tab-boundary\t\"",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := "Content-Type: " + tt.contentType + "\r\n\r\n" +
				"--unused\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nMust not be parsed\r\n"

			if _, err := parseRFC822(strings.NewReader(raw)); err == nil || !strings.Contains(err.Error(), "invalid MIME multipart boundary") {
				t.Fatalf("expected invalid boundary error, got %v", err)
			}
		})
	}
}

func TestParseRFC822AcceptsDecodedEncodedWordBoundary(t *testing.T) {
	const contentType = `multipart/mixed; boundary="=?UTF-8?Q?encoded=2Dboundary?="`
	const boundary = "encoded-boundary"
	raw := "Content-Type: " + contentType + "\r\n\r\n" +
		"--" + boundary + "\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nAccepted body\r\n" +
		"--" + boundary + "--\r\n"

	got, err := parseRFC822(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("parseRFC822: %v", err)
	}
	if got.body != "Accepted body" {
		t.Fatalf("body = %q, want %q", got.body, "Accepted body")
	}
}

func TestParseRFC822RejectsDecodedCarriageReturnBoundaryBeforeChildHeader(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
	}{
		{
			name:        "encoded word",
			contentType: `multipart/mixed; boundary="=?UTF-8?Q?x=0D?="`,
		},
		{
			name:        "RFC 2231",
			contentType: `multipart/mixed; boundary*=UTF-8''x%0D`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := "Content-Type: " + tt.contentType + "\r\n\r\n" +
				"--x\r\nX-Oversized: " + strings.Repeat("a", maxMIMEHeaderSize) + "\r\n\r\nbody\r\n" +
				"--x--\r\n"

			if _, err := parseRFC822(strings.NewReader(raw)); err == nil || !strings.Contains(err.Error(), "invalid MIME multipart boundary") {
				t.Fatalf("expected invalid boundary before child-header read, got %v", err)
			}
		})
	}
}

func TestValidateMIMEBoundary(t *testing.T) {
	tests := []struct {
		name     string
		boundary string
		valid    bool
	}{
		{name: "all letters and digits", boundary: "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz", valid: true},
		{name: "all allowed punctuation", boundary: "'()+_,-./:=?", valid: true},
		{name: "internal space", boundary: "one two", valid: true},
		{name: "length 70", boundary: strings.Repeat("a", 70), valid: true},
		{name: "empty", boundary: "", valid: false},
		{name: "length 71", boundary: strings.Repeat("a", 71), valid: false},
		{name: "trailing space", boundary: "boundary ", valid: false},
		{name: "trailing tab", boundary: "boundary\t", valid: false},
		{name: "internal tab", boundary: "one\ttwo", valid: false},
		{name: "carriage return", boundary: "boundary\r", valid: false},
		{name: "line feed", boundary: "boundary\n", valid: false},
		{name: "nul", boundary: "boundary\x00", valid: false},
		{name: "other control", boundary: "boundary\x1f", valid: false},
		{name: "delete", boundary: "boundary\x7f", valid: false},
		{name: "non ASCII", boundary: "boundaryé", valid: false},
		{name: "disallowed visible ASCII", boundary: "boundary%", valid: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateMIMEBoundary(tt.boundary)
			if (err == nil) != tt.valid {
				t.Fatalf("validateMIMEBoundary(%q) error = %v, valid = %v", tt.boundary, err, tt.valid)
			}
		})
	}
}

func TestParseRFC822RejectsOversizedChildHeaderAfterBufferSizedFragment(t *testing.T) {
	var raw strings.Builder
	raw.WriteString("Content-Type: multipart/mixed; boundary=x\r\n\r\n--x\r\n")
	// bufio.Reader's default 4096-byte buffer ends exactly before this field's
	// CRLF. The CRLF belongs to the fragmented field; it is not the blank line
	// that terminates the part header.
	raw.WriteString("X: ")
	raw.WriteString(strings.Repeat("a", 4093))
	raw.WriteString("\r\n")
	for raw.Len() < maxMIMEHeaderSize+8192 {
		raw.WriteString("Y: a\r\n")
	}
	raw.WriteString("\r\nbody\r\n--x--\r\n")

	if _, err := parseRFC822(strings.NewReader(raw.String())); err == nil || !strings.Contains(err.Error(), "header exceeds") {
		t.Fatalf("expected MIME part header error, got %v", err)
	}
}

func TestMultipartHeaderLimitReaderPreservesChunkBoundaryLines(t *testing.T) {
	for _, lineBytes := range []int{4095, 4096, 4097, 8191, 8192, 8193} {
		t.Run(fmt.Sprintf("line-%d", lineBytes), func(t *testing.T) {
			if lineBytes < len("X: ") {
				t.Fatal("invalid test case")
			}
			input := "--x\r\nX: " + strings.Repeat("a", lineBytes-len("X: ")) +
				"\r\nY: still-a-header\r\n\r\nbody\r\n--x--\r\n"
			got, err := io.ReadAll(newMultipartHeaderLimitReader(strings.NewReader(input), "x"))
			if err != nil {
				t.Fatalf("unexpected read error: %v", err)
			}
			if !bytes.Equal(got, []byte(input)) {
				t.Fatalf("reader changed input: got %d bytes, want %d", len(got), len(input))
			}
		})
	}
}

func TestMultipartHeaderLimitReaderPreservesNormalMessageAndSmallReads(t *testing.T) {
	input := "preamble\r\n--boundary\r\nContent-Type: text/plain\r\nX-Test: yes\r\n\r\nhello\r\n--boundary--\r\nepilogue"
	r := newMultipartHeaderLimitReader(strings.NewReader(input), "boundary")
	var got bytes.Buffer
	buffer := make([]byte, 7)
	for {
		n, err := r.Read(buffer)
		if n > 0 {
			got.Write(buffer[:n])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("unexpected read error after %d bytes: %v", got.Len(), err)
		}
		if n == 0 {
			t.Fatal("reader returned no progress without an error")
		}
	}
	if got.String() != input {
		t.Fatalf("reader changed normal multipart: got %q, want %q", got.String(), input)
	}
}

func TestParseRFC822SkipsMultipartAttachmentSubtree(t *testing.T) {
	raw := "Content-Type: multipart/mixed; boundary=outer\r\n\r\n" +
		"--outer\r\nContent-Type: multipart/alternative; boundary=attached\r\n" +
		"Content-Disposition: attachment; filename=forwarded.eml\r\n\r\n" +
		"--attached\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nATTACHED MUST NOT WIN\r\n" +
		"--attached--\r\n" +
		"--outer\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nReal body\r\n" +
		"--outer--\r\n"

	got, err := parseRFC822(strings.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if got.body != "Real body" {
		t.Fatalf("attached multipart leaked into body: %#v", got)
	}
}
