package mail

import (
	"bufio"
	"crypto/tls"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/emersion/go-imap"
)

type sharedFixture struct {
	uid          uint32
	to           string
	internalDate string
	subject      string
}

type sharedMockOptions struct {
	// configure runs only in the server goroutine; fixtures are copied before
	// each response so tests can simulate changed metadata between FETCHes.
	configure         func(*sharedFixture, bool)
	body              string
	bodyUIDs          chan uint32
	bodyBytes         chan int
	validityAfterBody uint32
}

var sharedFixtures = []sharedFixture{
	{5, "alias", "08-Oct-2026 00:00:05 +0000", "needle old UID"},
	{10, "alias", "07-Oct-2026 23:59:59 +0000", "needle old mail"},
	{11, "alias", "08-Oct-2026 00:00:00 +0000", "needle same second"},
	{12, "alias", "", "needle missing date"},
	{13, "other", "08-Oct-2026 00:00:01 +0000", "needle other alias"},
	{14, " alias", "08-Oct-2026 00:00:01 +0000", "needle quoted recipient"},
	{15, "alias", "08-Oct-2026 00:00:01 +0000", "needle eligible"},
	{16, "alias", "08-Oct-2026 00:00:02 +0000", "needle eligible second"},
	{17, "alias", "08-Oct-2026 00:00:03 +0000", "different subject"},
}

func startSharedMock(t *testing.T, validity, next uint32) (*Client, <-chan error) {
	return startSharedMockWithOptions(t, validity, next, sharedMockOptions{})
}

func startSharedMockWithOptions(t *testing.T, validity, next uint32, options sharedMockOptions) (*Client, <-chan error) {
	t.Helper()
	ln, serverTLS, clientTLS := newTestTLSListener(t)
	done := make(chan error, 1)
	go serveSharedMock(ln, serverTLS, validity, next, done, options)
	c := newTestClient(t, ln, clientTLS)
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c.forceClose()
		_ = ln.Close()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	return c, done
}

func testShareBoundary(c *Client) ShareBoundary {
	return ShareBoundary{MailboxIdentity: c.shareIdentity(), UIDValidity: 7, MinUID: 10, CreatedAt: time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)}
}

func TestSharedSearchFiltersHistoryBeforePagination(t *testing.T) {
	c, _ := startSharedMock(t, 7, 20)
	boundary := testShareBoundary(c)
	result, err := c.SearchShared(boundary, "alias@icloud.com", 2, 1, "needle")
	if err != nil {
		t.Fatal(err)
	}
	if result.Total != 2 || result.Page != 2 || len(result.Messages) != 1 || result.Messages[0].ID != "15" {
		t.Fatalf("history/recipient filtering or paging failed: %#v", result)
	}
	if result.Messages[0].Date != "2026-10-08T00:00:01Z" || result.Messages[0].Preview != "body 15" {
		t.Fatalf("unsafe date or missing preview: %#v", result.Messages[0])
	}
	result, err = c.SearchShared(boundary, "alias@icloud.com", 1, 20, "")
	if err != nil || result.Total != 3 {
		t.Fatalf("unsearched result: %#v, %v", result, err)
	}
}

func TestSharedFullRejectsOldUIDInternalDateAndRecipient(t *testing.T) {
	downloads := make(chan uint32, 16)
	c, _ := startSharedMockWithOptions(t, 7, 20, sharedMockOptions{bodyUIDs: downloads})
	boundary := testShareBoundary(c)
	for _, uid := range []uint32{0, 5, 10, 11, 12, 13, 14, 99} {
		if _, err := c.GetSharedFull(boundary, "alias@icloud.com", uid); err == nil {
			t.Errorf("UID %d escaped shared scope", uid)
		}
	}
	select {
	case uid := <-downloads:
		t.Fatalf("out-of-scope UID %d triggered BODY download", uid)
	default:
	}
	full, err := c.GetSharedFull(boundary, "alias@icloud.com", 15)
	if err != nil || full.Body != "body 15" || full.Date != "2026-10-08T00:00:01Z" {
		t.Fatalf("full=%#v err=%v", full, err)
	}
}

func TestSharedFullBoundsWireDownloadAndPreservesTruncatedMIME(t *testing.T) {
	for _, multipart := range []bool{false, true} {
		t.Run(fmt.Sprint(multipart), func(t *testing.T) {
			body := "Content-Type: text/plain\r\n\r\n" + strings.Repeat("x", maxSharedMessageBytes+100)
			if multipart {
				body = "Content-Type: multipart/mixed; boundary=bounded\r\n\r\n--bounded\r\nContent-Type: text/plain\r\n\r\ncustomer text\r\n--bounded\r\nContent-Type: application/octet-stream\r\nContent-Disposition: attachment; filename=large.bin\r\n\r\n" + strings.Repeat("x", maxSharedMessageBytes+100) + "\r\n--bounded--\r\n"
			}
			bytes := make(chan int, 1)
			c, _ := startSharedMockWithOptions(t, 7, 20, sharedMockOptions{body: body, bodyBytes: bytes})
			full, err := c.GetSharedFull(testShareBoundary(c), "alias@icloud.com", 15)
			if err != nil {
				t.Fatal(err)
			}
			if received := <-bytes; received != maxSharedMessageBytes {
				t.Fatalf("FETCH downloaded %d bytes, limit=%d", received, maxSharedMessageBytes)
			}
			if !full.BodyTruncated || len(full.Body) > maxPlainBodySize || len(full.HTMLBody) > maxHTMLBodySize {
				t.Fatalf("unbounded/truncation missing: plain=%d html=%d truncated=%v", len(full.Body), len(full.HTMLBody), full.BodyTruncated)
			}
			if multipart && full.Body != "customer text" {
				t.Fatalf("partial MIME lost text: %q", full.Body)
			}
		})
	}
}

func TestSharedFullRechecksBodyMetadataAndUIDValidity(t *testing.T) {
	for _, change := range []string{"uid", "date", "recipient", "uidvalidity"} {
		t.Run(change, func(t *testing.T) {
			options := sharedMockOptions{}
			if change == "uidvalidity" {
				options.validityAfterBody = 8
			} else {
				options.configure = func(f *sharedFixture, withBody bool) {
					if !withBody {
						return
					}
					switch change {
					case "uid":
						f.uid = 16
					case "date":
						f.internalDate = "08-Oct-2026 00:00:02 +0000"
					case "recipient":
						f.to = "other"
					}
				}
			}
			c, _ := startSharedMockWithOptions(t, 7, 20, options)
			full, err := c.GetSharedFull(testShareBoundary(c), "alias@icloud.com", 15)
			if err == nil || full != nil {
				t.Fatalf("%s change leaked body: %#v %v", change, full, err)
			}
		})
	}
}

func TestSharedUIDValidityAndCaptureFailClosed(t *testing.T) {
	for _, test := range []struct{ validity, next uint32 }{{0, 20}, {7, 0}, {8, 20}} {
		t.Run(fmt.Sprintf("%d-%d", test.validity, test.next), func(t *testing.T) {
			c, _ := startSharedMock(t, test.validity, test.next)
			if test.validity == 0 || test.next == 0 {
				if _, err := c.CaptureShareBoundary(); err == nil {
					t.Fatal("capture accepted missing UID metadata")
				}
			}
			if _, err := c.GetSharedFull(testShareBoundary(c), "alias@icloud.com", 15); err != ErrShareIdentityChanged {
				t.Fatalf("got %v", err)
			}
		})
	}
}

func TestSharedMailboxIdentityIncludesCredentialsAndTarget(t *testing.T) {
	c := NewClientWithServer("user@example.com", "password", "imap.example.com", 993)
	identity := c.shareIdentity()
	for _, change := range []func(){func() { c.username = "other@example.com" }, func() { c.server = "other.example.com" }, func() { c.port = 994 }, func() { c.password = "changed" }} {
		c.username, c.server, c.port, c.password = "user@example.com", "imap.example.com", 993, "password"
		change()
		if c.shareIdentity() == identity {
			t.Fatal("changed mailbox identity was retained")
		}
	}
}

func TestSharedActualClientIdentityFailsBeforeReadingChangedMailbox(t *testing.T) {
	c, _ := startSharedMock(t, 7, 20)
	boundary := testShareBoundary(c)
	c.password = "changed"
	if _, err := c.SearchShared(boundary, "alias@icloud.com", 1, 20, ""); err != ErrShareIdentityChanged {
		t.Fatalf("actual client identity change accepted: %v", err)
	}
}

func TestSharedBoundaryUsesInternalDateNotForgedSenderDate(t *testing.T) {
	boundary := ShareBoundary{MinUID: 10, CreatedAt: time.Date(2026, 10, 8, 0, 0, 0, 0, time.UTC)}
	msg := &imap.Message{Uid: 10, InternalDate: boundary.CreatedAt, Envelope: &imap.Envelope{Date: boundary.CreatedAt.AddDate(100, 0, 0), To: []*imap.Address{{MailboxName: "alias", HostName: "icloud.com"}}}}
	if sharedMessageAllowed(msg, boundary, "alias@icloud.com") {
		t.Fatal("same-second forged Date accepted")
	}
	msg.InternalDate = time.Time{}
	if sharedMessageAllowed(msg, boundary, "alias@icloud.com") {
		t.Fatal("missing internal date accepted")
	}
	msg.InternalDate = boundary.CreatedAt.Add(time.Second)
	msg.Envelope.Date = boundary.CreatedAt.AddDate(-100, 0, 0)
	if !sharedMessageAllowed(msg, boundary, "alias@icloud.com") {
		t.Fatal("sender Date affected authorization")
	}
}

func serveSharedMock(ln net.Listener, serverTLS *tls.Config, validity, next uint32, done chan<- error, options sharedMockOptions) {
	raw, err := ln.Accept()
	if err != nil {
		done <- err
		return
	}
	conn := tls.Server(raw, serverTLS)
	defer conn.Close()
	if err := conn.Handshake(); err != nil {
		done <- err
		return
	}
	reader := bufio.NewReader(conn)
	write := func(text string) error { _, err := conn.Write([]byte(text)); return err }
	if err := write("* OK [CAPABILITY IMAP4rev1] ready\r\n"); err != nil {
		done <- err
		return
	}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			done <- nil
			return
		} // keep alive through tagged response consumption
		upper := strings.ToUpper(line)
		tag := imapTag(line)
		response := ""
		switch {
		case strings.Contains(upper, " LOGIN "):
		case strings.Contains(upper, " EXAMINE "):
			response = fmt.Sprintf("* 9 EXISTS\r\n* OK [UIDVALIDITY %d] valid\r\n* OK [UIDNEXT %d] next\r\n", validity, next)
		case strings.Contains(upper, " UID SEARCH "):
			if !strings.Contains(upper, "UID 10:*") {
				done <- fmt.Errorf("missing UID boundary: %q", line)
				return
			}
			if strings.Contains(upper, "BODY") || strings.Contains(upper, "TEXT") {
				done <- fmt.Errorf("unexpected non-subject search: %q", line)
				return
			}
			response = "* SEARCH 5 10 11 12 13 14 15 16 17\r\n"
		case strings.Contains(upper, " UID FETCH "):
			fields := strings.Fields(line)
			set := new(imap.SeqSet)
			if err := set.Add(fields[3]); err != nil {
				done <- err
				return
			}
			withBody := strings.Contains(upper, "BODY.")
			if withBody && !strings.Contains(upper, "BODY.PEEK[]") {
				done <- fmt.Errorf("non-PEEK fetch: %q", line)
				return
			}
			if !strings.Contains(upper, "INTERNALDATE") {
				done <- fmt.Errorf("missing receipt date: %q", line)
				return
			}
			for _, fixture := range sharedFixtures {
				if !set.Contains(fixture.uid) {
					continue
				}
				if options.configure != nil {
					options.configure(&fixture, withBody)
				}
				envelope := fmt.Sprintf("ENVELOPE (\"08-Oct-2040 00:00:00 +0000\" %q ((NIL NIL \"sender\" \"example.com\")) NIL NIL ((NIL NIL %q \"icloud.com\")) NIL NIL NIL NIL)", fixture.subject, fixture.to)
				response += fmt.Sprintf("* %d FETCH (UID %d %s", fixture.uid, fixture.uid, envelope)
				if fixture.internalDate != "" {
					response += " INTERNALDATE " + strconv.Quote(fixture.internalDate)
				}
				if withBody {
					body := fmt.Sprintf("From: sender@example.com\r\nTo: alias@icloud.com\r\nSubject: needle\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nbody %d", fixture.uid)
					if options.body != "" {
						body = options.body
					}
					item := "BODY[]"
					partial := regexp.MustCompile(`<0\.(\d+)>`).FindStringSubmatch(upper)
					if len(partial) != 2 {
						done <- fmt.Errorf("shared body FETCH has no byte limit: %q", line)
						return
					}
					if len(partial) == 2 {
						limit, _ := strconv.Atoi(partial[1])
						body = body[:min(len(body), limit)]
						item += "<0>"
					}
					if options.bodyUIDs != nil {
						options.bodyUIDs <- fixture.uid
					}
					if options.bodyBytes != nil {
						options.bodyBytes <- len(body)
					}
					if options.validityAfterBody != 0 {
						validity = options.validityAfterBody
					}
					response += fmt.Sprintf(" %s {%d}\r\n%s", item, len(body), body)
				}
				response += ")\r\n"
			}
		default:
			done <- fmt.Errorf("shared reader issued unexpected/mutating command %q", line)
			return
		}
		if err := write(response + tag + " OK completed\r\n"); err != nil {
			done <- err
			return
		}
	}
}
