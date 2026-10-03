package mail

import (
	"bufio"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"math/big"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-imap"
)

func TestMessageWithinDaysAcceptsRFC3339AndFiltersOldMail(t *testing.T) {
	now := time.Date(2026, time.October, 3, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		raw  string
		days int
		want bool
	}{
		{name: "recent RFC3339", raw: now.Add(-23 * time.Hour).Format(time.RFC3339), days: 1, want: true},
		{name: "old RFC3339", raw: now.Add(-25 * time.Hour).Format(time.RFC3339), days: 1, want: false},
		{name: "legacy RFC1123Z", raw: now.Add(-23 * time.Hour).Format(time.RFC1123Z), days: 1, want: true},
		{name: "unlimited", raw: now.Add(-365 * 24 * time.Hour).Format(time.RFC3339), days: 0, want: true},
		{name: "unknown date retained", raw: "not-a-date", days: 1, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := messageWithinDays(tt.raw, tt.days, now); got != tt.want {
				t.Fatalf("messageWithinDays(%q, %d) = %v, want %v", tt.raw, tt.days, got, tt.want)
			}
		})
	}
}

func TestNormalizeRecipientsDeduplicatesCaseAndWhitespace(t *testing.T) {
	got := normalizeRecipients([]string{" Alpha@iCloud.com ", "alpha@icloud.com", "", "beta@icloud.com"})
	want := []string{"alpha@icloud.com", "beta@icloud.com"}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("normalizeRecipients() = %v, want %v", got, want)
	}
}

func TestRecipientSearchCriteriaUsesBalancedOrAndSince(t *testing.T) {
	now := time.Date(2026, time.October, 3, 12, 0, 0, 0, time.UTC)
	criteria := recipientSearchCriteria([]string{"a@icloud.com", "b@icloud.com", "c@icloud.com"}, 7, now)
	if !criteria.Since.Equal(now.AddDate(0, 0, -7)) {
		t.Fatalf("Since = %v", criteria.Since)
	}
	if len(criteria.Or) != 1 || criteria.Or[0][0] == nil || criteria.Or[0][1] == nil {
		t.Fatalf("criteria.Or = %#v, want nested OR", criteria.Or)
	}
}

func TestRecipientTextSearchCriteriaCombinesScopeAndServerSearch(t *testing.T) {
	now := time.Date(2026, time.October, 3, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		field string
		check func(*imap.SearchCriteria) bool
	}{
		{field: "all", check: func(c *imap.SearchCriteria) bool { return fmt.Sprint(c.Text) == "[needle]" }},
		{field: "subject", check: func(c *imap.SearchCriteria) bool { return c.Header.Get("Subject") == "needle" }},
		{field: "from", check: func(c *imap.SearchCriteria) bool { return c.Header.Get("From") == "needle" }},
		{field: "to", check: func(c *imap.SearchCriteria) bool { return c.Header.Get("To") == "needle" }},
		{field: "body", check: func(c *imap.SearchCriteria) bool { return fmt.Sprint(c.Body) == "[needle]" }},
	}
	for _, tt := range tests {
		t.Run(tt.field, func(t *testing.T) {
			criteria := recipientTextSearchCriteria([]string{"a@icloud.com", "b@icloud.com"}, "needle", tt.field, 0, now)
			if len(criteria.Or) != 1 {
				t.Fatalf("recipient OR scope missing: %#v", criteria)
			}
			if !criteria.Since.IsZero() {
				t.Fatalf("Since = %v, want full history", criteria.Since)
			}
			if !tt.check(criteria) {
				t.Fatalf("criteria = %#v, search field %q missing", criteria, tt.field)
			}
		})
	}
}

func TestNormalizeUIDsSortsDeduplicatesAndLimitsNewest(t *testing.T) {
	got := normalizeUIDs([]uint32{7, 2, 7, 9, 3, 0}, 3)
	want := []uint32{3, 7, 9}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("normalizeUIDs() = %v, want %v", got, want)
	}
}

func TestEnvelopeMatchesRecipientsUsesExactNormalizedAddress(t *testing.T) {
	envelope := &imap.Envelope{To: []*imap.Address{{MailboxName: "Alpha", HostName: "iCloud.com"}}}
	if !envelopeMatchesRecipients(envelope, map[string]struct{}{"alpha@icloud.com": {}}) {
		t.Fatal("exact address did not match")
	}
	if envelopeMatchesRecipients(envelope, map[string]struct{}{"pha@icloud.com": {}}) {
		t.Fatal("substring address matched")
	}
	quotedComma := &imap.Envelope{To: []*imap.Address{{MailboxName: "evil,alias@icloud.com,other", HostName: "example.com"}}}
	if envelopeMatchesRecipients(quotedComma, map[string]struct{}{"alias@icloud.com": {}}) {
		t.Fatal("comma inside a quoted local-part produced a false HME match")
	}
	quotedLeadingSpace := &imap.Envelope{To: []*imap.Address{{MailboxName: " alias", HostName: "icloud.com"}}}
	if envelopeMatchesRecipients(quotedLeadingSpace, map[string]struct{}{"alias@icloud.com": {}}) {
		t.Fatal("leading space inside a quoted local-part produced a false HME match")
	}
}

func TestScopedMessageOperationsRejectEmptyRecipientSetWithoutConnecting(t *testing.T) {
	c := &Client{}
	if _, err := c.GetFullForRecipients(42, nil); !errors.Is(err, ErrMessageOutsideHME) {
		t.Fatalf("GetFullForRecipients error = %v, want ErrMessageOutsideHME", err)
	}
	if err := c.DeleteForRecipients(42, nil); !errors.Is(err, ErrMessageOutsideHME) {
		t.Fatalf("DeleteForRecipients error = %v, want ErrMessageOutsideHME", err)
	}
}

func TestClientConnectTimesOutWaitingForGreeting(t *testing.T) {
	useIMAPTimeouts(t, 100*time.Millisecond, 100*time.Millisecond)
	ln, serverTLS, clientTLS := newTestTLSListener(t)

	tlsReady := make(chan struct{})
	release := make(chan struct{})
	serverDone := make(chan error, 1)
	var releaseOnce sync.Once
	stopServer := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(func() {
		stopServer()
		_ = ln.Close()
		select {
		case <-serverDone:
		case <-time.After(time.Second):
			t.Error("stalled greeting test server did not stop")
		}
	})

	go func() {
		raw, err := ln.Accept()
		if err != nil {
			serverDone <- err
			return
		}
		conn := tls.Server(raw, serverTLS)
		defer conn.Close()
		if err := conn.Handshake(); err != nil {
			serverDone <- err
			return
		}
		close(tlsReady)
		<-release // Complete TLS, but deliberately never send an IMAP greeting.
		serverDone <- nil
	}()

	c := newTestClient(t, ln, clientTLS)
	connectDone := make(chan error, 1)
	started := time.Now()
	go func() { connectDone <- c.Connect() }()

	select {
	case <-tlsReady:
	case <-time.After(time.Second):
		stopServer()
		t.Fatal("server did not complete the TLS handshake")
	}

	var err error
	select {
	case err = <-connectDone:
	case <-time.After(time.Second):
		stopServer()
		<-connectDone
		t.Fatal("Connect remained blocked after its configured greeting timeout")
	}
	stopServer()
	if err == nil {
		t.Fatal("Connect succeeded without receiving an IMAP greeting")
	}
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("Connect error = %v, want a network timeout", err)
	}
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("Connect returned after %v, want less than 1s", elapsed)
	}
}

func TestClientConnectTimesOutWaitingForInitialCapability(t *testing.T) {
	useIMAPTimeouts(t, 100*time.Millisecond, 100*time.Millisecond)
	ln, serverTLS, clientTLS := newTestTLSListener(t)

	capabilitySeen := make(chan string, 1)
	release := make(chan struct{})
	serverDone := make(chan error, 1)
	var releaseOnce sync.Once
	stopServer := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(func() {
		stopServer()
		_ = ln.Close()
		select {
		case <-serverDone:
		case <-time.After(time.Second):
			t.Error("stalled CAPABILITY test server did not stop")
		}
	})

	go func() {
		raw, err := ln.Accept()
		if err != nil {
			serverDone <- err
			return
		}
		conn := tls.Server(raw, serverTLS)
		defer conn.Close()
		if err := conn.Handshake(); err != nil {
			serverDone <- err
			return
		}
		reader := bufio.NewReader(conn)
		if _, err := conn.Write([]byte("* OK ready\r\n")); err != nil {
			serverDone <- err
			return
		}
		capability, err := reader.ReadString('\n')
		if err != nil {
			serverDone <- err
			return
		}
		capabilitySeen <- capability
		<-release // Deliberately never complete the bootstrap CAPABILITY command.
		serverDone <- nil
	}()

	c := newTestClient(t, ln, clientTLS)
	connectDone := make(chan error, 1)
	started := time.Now()
	go func() { connectDone <- c.Connect() }()

	select {
	case line := <-capabilitySeen:
		if !strings.Contains(strings.ToUpper(line), "CAPABILITY") {
			stopServer()
			t.Fatalf("server received %q, want CAPABILITY", line)
		}
	case <-time.After(time.Second):
		stopServer()
		t.Fatal("server did not receive the bootstrap CAPABILITY command")
	}

	var err error
	select {
	case err = <-connectDone:
	case <-time.After(time.Second):
		stopServer()
		<-connectDone
		t.Fatal("Connect remained blocked after its bootstrap CAPABILITY timeout")
	}
	stopServer()
	if err == nil {
		t.Fatal("Connect succeeded without receiving a CAPABILITY response")
	}
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("Connect returned after %v, want less than 1s", elapsed)
	}
}

func TestUIDFetchDrainsDuplicateResponses(t *testing.T) {
	for _, tc := range []struct {
		name string
		call func(*Client) (string, error)
	}{
		{
			name: "fetchOneUID",
			call: func(c *Client) (string, error) {
				if _, err := c.cli.Select("INBOX", true); err != nil {
					return "", err
				}
				msg, err := c.fetchOneUID(42)
				return msg.Preview, err
			},
		},
		{
			name: "GetFull",
			call: func(c *Client) (string, error) {
				msg, err := c.GetFull(42)
				if err != nil {
					return "", err
				}
				return msg.Body, nil
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			useIMAPTimeouts(t, time.Second, 100*time.Millisecond)
			ln, serverTLS, clientTLS := newTestTLSListener(t)
			release := make(chan struct{})
			serverDone := make(chan error, 1)
			var releaseOnce sync.Once
			stopServer := func() { releaseOnce.Do(func() { close(release) }) }
			t.Cleanup(func() {
				stopServer()
				_ = ln.Close()
				select {
				case err := <-serverDone:
					if err != nil {
						t.Errorf("duplicate FETCH test server: %v", err)
					}
				case <-time.After(time.Second):
					t.Error("duplicate FETCH test server did not stop")
				}
			})

			go serveDuplicateUIDFetch(ln, serverTLS, release, serverDone)

			c := newTestClient(t, ln, clientTLS)
			if err := c.Connect(); err != nil {
				t.Fatalf("Connect: %v", err)
			}
			t.Cleanup(c.forceClose)

			result := make(chan struct {
				body string
				err  error
			}, 1)
			go func() {
				body, err := tc.call(c)
				result <- struct {
					body string
					err  error
				}{body: body, err: err}
			}()

			select {
			case got := <-result:
				if got.err != nil {
					t.Fatalf("%s: %v", tc.name, got.err)
				}
				if got.body != "first body" {
					t.Fatalf("%s body = %q, want first valid response", tc.name, got.body)
				}
			case <-time.After(time.Second):
				t.Fatalf("%s remained blocked by duplicate FETCH responses", tc.name)
			}
			stopServer()
		})
	}
}

func TestDeleteForRecipientsUsesUIDExpungeForOnlyTarget(t *testing.T) {
	useIMAPTimeouts(t, time.Second, time.Second)
	ln, serverTLS, clientTLS := newTestTLSListener(t)
	serverDone := make(chan error, 1)
	commands := make(chan []string, 1)
	go serveScopedDelete(ln, serverTLS, commands, serverDone)

	c := newTestClient(t, ln, clientTLS)
	if err := c.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(c.forceClose)
	if err := c.DeleteForRecipients(42, []string{"alias@icloud.com"}); err != nil {
		t.Fatalf("DeleteForRecipients: %v", err)
	}

	got := <-commands
	joined := strings.ToUpper(strings.Join(got, "\n"))
	if !strings.Contains(joined, "UID STORE 42") {
		t.Fatalf("commands = %q, missing UID STORE 42", got)
	}
	if !strings.Contains(joined, "UID EXPUNGE 42") {
		t.Fatalf("commands = %q, missing UID EXPUNGE 42", got)
	}
	for _, command := range got {
		fields := strings.Fields(strings.ToUpper(command))
		if len(fields) > 1 && fields[1] == "EXPUNGE" {
			t.Fatalf("unsafe unscoped EXPUNGE sent: %q", command)
		}
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestScopedMessageOperationsRejectLeadingSpaceQuotedLocalPart(t *testing.T) {
	tests := []struct {
		name      string
		operation string
		run       func(*testing.T, *Client)
	}{
		{
			name:      "list",
			operation: "list",
			run: func(t *testing.T, c *Client) {
				messages, err := c.FindByRecipients([]string{" alias@icloud.com "}, 10, 0)
				if err != nil {
					t.Fatalf("FindByRecipients: %v", err)
				}
				if len(messages) != 0 {
					t.Fatalf("messages = %#v, want no authorized matches", messages)
				}
			},
		},
		{
			name:      "detail",
			operation: "detail",
			run: func(t *testing.T, c *Client) {
				message, err := c.GetFullForRecipients(42, []string{" alias@icloud.com "})
				if !errors.Is(err, ErrMessageOutsideHME) {
					t.Fatalf("GetFullForRecipients() = %#v, %v; want ErrMessageOutsideHME", message, err)
				}
			},
		},
		{
			name:      "delete",
			operation: "delete",
			run: func(t *testing.T, c *Client) {
				if err := c.DeleteForRecipients(42, []string{" alias@icloud.com "}); !errors.Is(err, ErrMessageOutsideHME) {
					t.Fatalf("DeleteForRecipients() error = %v, want ErrMessageOutsideHME", err)
				}
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			useIMAPTimeouts(t, time.Second, time.Second)
			ln, serverTLS, clientTLS := newTestTLSListener(t)
			serverDone := make(chan error, 1)
			go serveLeadingSpaceScopedOperation(ln, serverTLS, tc.operation, serverDone)

			c := newTestClient(t, ln, clientTLS)
			if err := c.Connect(); err != nil {
				t.Fatalf("Connect: %v", err)
			}
			defer func() {
				c.forceClose()
				_ = ln.Close()
				select {
				case err := <-serverDone:
					if err != nil {
						t.Errorf("fake IMAP server: %v", err)
					}
				case <-time.After(time.Second):
					t.Error("fake IMAP server did not stop")
				}
			}()
			tc.run(t, c)
		})
	}
}

func TestClientFetchTimeoutClosesChannels(t *testing.T) {
	useIMAPTimeouts(t, time.Second, 100*time.Millisecond)
	ln, serverTLS, clientTLS := newTestTLSListener(t)

	fetchSeen := make(chan string, 1)
	release := make(chan struct{})
	serverDone := make(chan error, 1)
	var releaseOnce sync.Once
	stopServer := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(func() {
		stopServer()
		_ = ln.Close()
		select {
		case <-serverDone:
		case <-time.After(time.Second):
			t.Error("fetch timeout test server did not stop")
		}
	})

	go func() {
		raw, err := ln.Accept()
		if err != nil {
			serverDone <- err
			return
		}
		conn := tls.Server(raw, serverTLS)
		defer conn.Close()
		if err := conn.Handshake(); err != nil {
			serverDone <- err
			return
		}
		reader := bufio.NewReader(conn)
		if _, err := conn.Write([]byte("* OK [CAPABILITY IMAP4rev1] ready\r\n")); err != nil {
			serverDone <- err
			return
		}
		login, err := reader.ReadString('\n')
		if err != nil {
			serverDone <- err
			return
		}
		if _, err := conn.Write([]byte(imapTag(login) + " OK LOGIN completed\r\n")); err != nil {
			serverDone <- err
			return
		}
		selectCmd, err := reader.ReadString('\n')
		if err != nil {
			serverDone <- err
			return
		}
		selectReply := "* 1 EXISTS\r\n" + imapTag(selectCmd) + " OK [READ-ONLY] SELECT completed\r\n"
		if _, err := conn.Write([]byte(selectReply)); err != nil {
			serverDone <- err
			return
		}
		fetch, err := reader.ReadString('\n')
		if err != nil {
			serverDone <- err
			return
		}
		fetchSeen <- fetch
		<-release // Never finish FETCH; the client command deadline must end it.
		serverDone <- nil
	}()

	c := newTestClient(t, ln, clientTLS)
	if err := c.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(c.forceClose)
	if c.cli.Timeout != imapCommandTimeout {
		t.Fatalf("client command timeout = %v, want %v", c.cli.Timeout, imapCommandTimeout)
	}

	fetchDone := make(chan error, 1)
	go func() {
		_, err := c.ListInbox(1, 0)
		fetchDone <- err
	}()
	select {
	case line := <-fetchSeen:
		if !strings.Contains(strings.ToUpper(line), "FETCH") {
			t.Fatalf("server received %q, want FETCH", line)
		}
	case <-time.After(time.Second):
		stopServer()
		t.Fatal("server did not receive FETCH")
	}

	select {
	case err := <-fetchDone:
		if err == nil {
			t.Fatal("ListInbox succeeded although FETCH never received a response")
		}
		// go-imap's background reader logs the underlying i/o timeout and
		// surfaces it to Fetch as ErrConnectionClosed. The important contract
		// here is that Fetch closes its message channel and the caller returns.
		if !strings.Contains(strings.ToLower(err.Error()), "connection closed") {
			t.Fatalf("ListInbox error = %v, want a closed connection after timeout", err)
		}
		if !isLikelyConnErr(err) {
			t.Fatalf("timed-out FETCH error %q would not be evicted from the pool", err)
		}
	case <-time.After(time.Second):
		stopServer()
		<-fetchDone
		t.Fatal("ListInbox goroutine/channel remained blocked after command timeout")
	}
	stopServer()
}

func TestSearchByRecipientsSearchesFullInboxAndPaginatesVerifiedMatches(t *testing.T) {
	useIMAPTimeouts(t, time.Second, 3*time.Second)
	ln, serverTLS, clientTLS := newTestTLSListener(t)
	release := make(chan struct{})
	serverDone := make(chan error, 1)
	var releaseOnce sync.Once
	stopServer := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(func() {
		stopServer()
		_ = ln.Close()
		select {
		case err := <-serverDone:
			if err != nil {
				t.Errorf("search pagination test server: %v", err)
			}
		case <-time.After(time.Second):
			t.Error("search pagination test server did not stop")
		}
	})
	go serveSearchPagination(ln, serverTLS, []int{1}, release, serverDone)

	c := newTestClient(t, ln, clientTLS)
	if err := c.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(c.forceClose)
	result, err := c.SearchByRecipients([]string{"alias@icloud.com"}, 999, 2, "needle", "all", 0)
	if err != nil {
		t.Fatalf("SearchByRecipients: %v", err)
	}
	if result.Total != 203 || result.Page != 102 || result.PageSize != 2 {
		t.Fatalf("result metadata = %#v, want total=203 clamped page=102 page_size=2", result)
	}
	if len(result.Messages) != 1 || result.Messages[0].ID != "1" {
		t.Fatalf("messages = %#v, want final global page UID 1", result.Messages)
	}
	stopServer()
}

func TestSearchByRecipientsEmptyResultUsesFirstPageAndNonNilMessages(t *testing.T) {
	useIMAPTimeouts(t, time.Second, time.Second)
	ln, serverTLS, clientTLS := newTestTLSListener(t)
	serverDone := make(chan error, 1)
	go serveEmptySearch(ln, serverTLS, serverDone)
	c := newTestClient(t, ln, clientTLS)
	if err := c.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	result, err := c.SearchByRecipients([]string{"alias@icloud.com"}, 9, 20, "missing", "subject", 0)
	c.forceClose()
	_ = ln.Close()
	if err != nil {
		t.Fatalf("SearchByRecipients: %v", err)
	}
	if result.Total != 0 || result.Page != 1 || result.PageSize != 20 || result.Messages == nil || len(result.Messages) != 0 {
		t.Fatalf("empty result = %#v", result)
	}
	if serverErr := <-serverDone; serverErr != nil {
		t.Fatal(serverErr)
	}
}

func TestSearchByRecipientsFailsClosedOnSearchOrEnvelopeFetchError(t *testing.T) {
	for _, stage := range []string{"search", "fetch"} {
		t.Run(stage, func(t *testing.T) {
			useIMAPTimeouts(t, time.Second, time.Second)
			ln, serverTLS, clientTLS := newTestTLSListener(t)
			serverDone := make(chan error, 1)
			go serveSearchFailure(ln, serverTLS, stage, serverDone)
			c := newTestClient(t, ln, clientTLS)
			if err := c.Connect(); err != nil {
				t.Fatalf("Connect: %v", err)
			}
			_, err := c.SearchByRecipients([]string{"alias@icloud.com"}, 1, 20, "", "all", 0)
			c.forceClose()
			_ = ln.Close()
			if err == nil {
				t.Fatalf("SearchByRecipients succeeded on %s failure", stage)
			}
			if serverErr := <-serverDone; serverErr != nil {
				t.Fatal(serverErr)
			}
		})
	}
}

func serveSearchPagination(ln net.Listener, serverTLS *tls.Config, pageUIDs []int, release <-chan struct{}, done chan<- error) {
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
	write := func(response string) bool {
		if _, writeErr := conn.Write([]byte(response)); writeErr != nil {
			done <- writeErr
			return false
		}
		return true
	}
	read := func(want string) (string, bool) {
		line, readErr := reader.ReadString('\n')
		if readErr != nil {
			done <- readErr
			return "", false
		}
		if !strings.Contains(strings.ToUpper(line), strings.ToUpper(want)) {
			done <- fmt.Errorf("received %q, want %q", line, want)
			return "", false
		}
		return line, true
	}
	if !write("* OK [CAPABILITY IMAP4rev1] ready\r\n") {
		return
	}
	login, ok := read("LOGIN")
	if !ok || !write(imapTag(login)+" OK LOGIN completed\r\n") {
		return
	}
	selectCmd, ok := read("INBOX")
	if !ok || !write("* 300 EXISTS\r\n"+imapTag(selectCmd)+" OK [READ-ONLY] completed\r\n") {
		return
	}
	search, ok := read("UID SEARCH")
	if !ok {
		return
	}
	upperSearch := strings.ToUpper(search)
	if !strings.Contains(upperSearch, "TO ") || !strings.Contains(upperSearch, "ALIAS@ICLOUD.COM") || !strings.Contains(upperSearch, "TEXT ") || !strings.Contains(upperSearch, "NEEDLE") || strings.Contains(upperSearch, "SINCE") {
		done <- fmt.Errorf("unexpected full-history search command %q", search)
		return
	}
	uidTexts := make([]string, 0, 206)
	for uid := 1; uid <= 205; uid++ {
		uidTexts = append(uidTexts, strconv.Itoa(uid))
	}
	uidTexts = append(uidTexts, "205") // prove duplicate SEARCH UIDs are normalized
	if !write("* SEARCH " + strings.Join(uidTexts, " ") + "\r\n" + imapTag(search) + " OK SEARCH completed\r\n") {
		return
	}

	for batch := 0; batch < 2; batch++ {
		fetch, ok := read("UID FETCH")
		if !ok {
			return
		}
		if strings.Contains(strings.ToUpper(fetch), "BODY") {
			done <- fmt.Errorf("candidate verification downloaded bodies: %q", fetch)
			return
		}
		start, end := 1, 200
		if batch == 1 {
			start, end = 201, 205
		}
		if batch == 0 {
			if !write(searchEnvelopeResponse(999, "alias", "icloud.com")) {
				return
			}
		}
		for uid := start; uid <= end; uid++ {
			mailbox, host := "alias", "icloud.com"
			if uid == 50 {
				mailbox, host = "normal", "example.com"
			} else if uid == 51 {
				mailbox = " alias" // a distinct quoted local-part must not authorize
			}
			if !write(searchEnvelopeResponse(uid, mailbox, host)) {
				return
			}
			if uid == 1 && !write(searchEnvelopeResponse(uid, "normal", "example.com")) {
				return // duplicate response must not overwrite the first usable envelope
			}
		}
		if !write(imapTag(fetch) + " OK FETCH completed\r\n") {
			return
		}
	}

	fetch, ok := read("UID FETCH ")
	if !ok {
		return
	}
	fields := strings.Fields(fetch)
	set := new(imap.SeqSet)
	if len(fields) < 4 || set.Add(fields[3]) != nil || !strings.Contains(fetch, "BODY.PEEK[]<0.32768>") {
		done <- fmt.Errorf("unexpected page preview command %q", fetch)
		return
	}
	for _, uid := range pageUIDs {
		if !set.Contains(uint32(uid)) {
			done <- fmt.Errorf("page UID %d missing from %q", uid, fetch)
			return
		}
		body := fmt.Sprintf("From: sender@example.com\r\nTo: alias@icloud.com\r\nSubject: needle %d\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nbody %d", uid, uid)
		envelope := fmt.Sprintf("ENVELOPE (\"03-Oct-2025 02:00:00 +0000\" \"needle %d\" ((NIL NIL \"sender\" \"example.com\")) NIL NIL ((NIL NIL \"alias\" \"icloud.com\")) NIL NIL NIL NIL)", uid)
		response := fmt.Sprintf("* %d FETCH (UID %d %s INTERNALDATE \"03-Oct-2025 02:00:00 +0000\" BODY[]<0> {%d}\r\n%s)\r\n", uid, uid, envelope, len(body), body)
		if !write(response) {
			return
		}
	}
	if !write(imapTag(fetch) + " OK FETCH completed\r\n") {
		return
	}
	<-release
	done <- nil
}

func TestSearchPageFetchesOrderedPreviewsInSingleBoundedCommand(t *testing.T) {
	useIMAPTimeouts(t, time.Second, 3*time.Second)
	ln, serverTLS, clientTLS := newTestTLSListener(t)
	release := make(chan struct{})
	done := make(chan error, 1)
	go serveSearchPagination(ln, serverTLS, []int{205, 204}, release, done)
	c := newTestClient(t, ln, clientTLS)
	defer func() {
		c.forceClose()
		close(release)
		_ = ln.Close()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	if err := c.Connect(); err != nil {
		t.Fatal(err)
	}
	result, err := c.SearchByRecipients([]string{"alias@icloud.com"}, 1, 2, "needle", "all", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Messages) != 2 || result.Messages[0].ID != "205" || result.Messages[1].ID != "204" || result.Messages[0].Preview != "body 205" {
		t.Fatalf("page messages = %#v", result.Messages)
	}
}

func serveEmptySearch(ln net.Listener, serverTLS *tls.Config, done chan<- error) {
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
	if _, err = conn.Write([]byte("* OK [CAPABILITY IMAP4rev1] ready\r\n")); err != nil {
		done <- err
		return
	}
	login, err := reader.ReadString('\n')
	if err != nil {
		done <- err
		return
	}
	_, _ = conn.Write([]byte(imapTag(login) + " OK LOGIN completed\r\n"))
	selectCmd, err := reader.ReadString('\n')
	if err != nil {
		done <- err
		return
	}
	_, _ = conn.Write([]byte("* 0 EXISTS\r\n" + imapTag(selectCmd) + " OK completed\r\n"))
	search, err := reader.ReadString('\n')
	if err != nil {
		done <- err
		return
	}
	upperSearch := strings.ToUpper(search)
	if !strings.Contains(upperSearch, "SUBJECT ") || !strings.Contains(upperSearch, "MISSING") {
		done <- fmt.Errorf("received %q, want SUBJECT search", search)
		return
	}
	_, err = conn.Write([]byte("* SEARCH\r\n" + imapTag(search) + " OK SEARCH completed\r\n"))
	done <- err
}

func searchEnvelopeResponse(uid int, mailbox, host string) string {
	envelope := fmt.Sprintf("ENVELOPE (\"03-Oct-2025 02:00:00 +0000\" \"needle %d\" ((NIL NIL \"sender\" \"example.com\")) NIL NIL ((NIL NIL %q %q)) NIL NIL NIL NIL)", uid, mailbox, host)
	return fmt.Sprintf("* %d FETCH (UID %d %s)\r\n", uid, uid, envelope)
}

func serveSearchFailure(ln net.Listener, serverTLS *tls.Config, stage string, done chan<- error) {
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
	if _, err = conn.Write([]byte("* OK [CAPABILITY IMAP4rev1] ready\r\n")); err != nil {
		done <- err
		return
	}
	login, err := reader.ReadString('\n')
	if err != nil {
		done <- err
		return
	}
	_, _ = conn.Write([]byte(imapTag(login) + " OK LOGIN completed\r\n"))
	selectCmd, err := reader.ReadString('\n')
	if err != nil {
		done <- err
		return
	}
	_, _ = conn.Write([]byte("* 1 EXISTS\r\n" + imapTag(selectCmd) + " OK completed\r\n"))
	search, err := reader.ReadString('\n')
	if err != nil {
		done <- err
		return
	}
	if stage == "search" {
		_, err = conn.Write([]byte(imapTag(search) + " NO SEARCH failed\r\n"))
		done <- err
		return
	}
	_, _ = conn.Write([]byte("* SEARCH 1\r\n" + imapTag(search) + " OK SEARCH completed\r\n"))
	fetch, err := reader.ReadString('\n')
	if err != nil {
		done <- err
		return
	}
	_, err = conn.Write([]byte(imapTag(fetch) + " NO FETCH failed\r\n"))
	done <- err
}

func serveDuplicateUIDFetch(ln net.Listener, serverTLS *tls.Config, release <-chan struct{}, done chan<- error) {
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
	if _, err := conn.Write([]byte("* OK [CAPABILITY IMAP4rev1] ready\r\n")); err != nil {
		done <- err
		return
	}
	login, err := reader.ReadString('\n')
	if err != nil {
		done <- err
		return
	}
	if _, err := conn.Write([]byte(imapTag(login) + " OK LOGIN completed\r\n")); err != nil {
		done <- err
		return
	}
	selectCmd, err := reader.ReadString('\n')
	if err != nil {
		done <- err
		return
	}
	selectReply := "* 1 EXISTS\r\n" + imapTag(selectCmd) + " OK [READ-ONLY] SELECT completed\r\n"
	if _, err := conn.Write([]byte(selectReply)); err != nil {
		done <- err
		return
	}
	fetch, err := reader.ReadString('\n')
	if err != nil {
		done <- err
		return
	}
	if !strings.Contains(strings.ToUpper(fetch), "UID FETCH 42") {
		done <- fmt.Errorf("received %q, want UID FETCH 42", fetch)
		return
	}

	bodyItem := "BODY[]"
	if strings.Contains(strings.ToUpper(fetch), "RFC822") {
		bodyItem = "RFC822"
	}
	for i, body := range []string{"first body", "second body", "third body"} {
		rfc822 := "From: sender@example.com\r\n" +
			"To: recipient@example.com\r\n" +
			"Subject: duplicate response\r\n" +
			"Content-Type: text/plain; charset=utf-8\r\n\r\n" + body
		response := fmt.Sprintf(
			"* 1 FETCH (UID 42 ENVELOPE (\"03-Oct-2026 02:00:00 +0000\" \"subject-%d\" NIL NIL NIL NIL NIL NIL NIL NIL) INTERNALDATE \"03-Oct-2026 02:00:00 +0000\" %s {%d}\r\n%s)\r\n",
			i+1, bodyItem, len(rfc822), rfc822,
		)
		if _, err := conn.Write([]byte(response)); err != nil {
			done <- err
			return
		}
	}
	if _, err := conn.Write([]byte(imapTag(fetch) + " OK FETCH completed\r\n")); err != nil {
		done <- err
		return
	}
	<-release
	done <- nil
}

func serveScopedDelete(ln net.Listener, serverTLS *tls.Config, commands chan<- []string, done chan<- error) {
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
	if _, err := conn.Write([]byte("* OK [CAPABILITY IMAP4rev1 UIDPLUS] ready\r\n")); err != nil {
		done <- err
		return
	}
	var seen []string
	read := func() (string, error) {
		line, err := reader.ReadString('\n')
		if err == nil {
			seen = append(seen, line)
		}
		return line, err
	}
	login, err := read()
	if err != nil {
		done <- err
		return
	}
	if _, err := conn.Write([]byte(imapTag(login) + " OK LOGIN completed\r\n")); err != nil {
		done <- err
		return
	}
	selectCmd, err := read()
	if err != nil {
		done <- err
		return
	}
	if _, err := conn.Write([]byte("* 2 EXISTS\r\n" + imapTag(selectCmd) + " OK [READ-WRITE] SELECT completed\r\n")); err != nil {
		done <- err
		return
	}
	fetch, err := read()
	if err != nil {
		done <- err
		return
	}
	if strings.Contains(strings.ToUpper(fetch), "CAPABILITY") {
		if _, err := conn.Write([]byte("* CAPABILITY IMAP4rev1 UIDPLUS\r\n" + imapTag(fetch) + " OK CAPABILITY completed\r\n")); err != nil {
			done <- err
			return
		}
		fetch, err = read()
		if err != nil {
			done <- err
			return
		}
	}
	envelope := "* 1 FETCH (UID 42 ENVELOPE (\"03-Oct-2026 02:00:00 +0000\" \"subject\" ((NIL NIL \"sender\" \"example.com\")) NIL NIL ((NIL NIL \"alias\" \"icloud.com\")) NIL NIL NIL NIL))\r\n"
	if _, err := conn.Write([]byte(envelope + imapTag(fetch) + " OK FETCH completed\r\n")); err != nil {
		done <- err
		return
	}
	store, err := read()
	if err != nil {
		done <- err
		return
	}
	if strings.Contains(strings.ToUpper(store), "CAPABILITY") {
		if _, err := conn.Write([]byte("* CAPABILITY IMAP4rev1 UIDPLUS\r\n" + imapTag(store) + " OK CAPABILITY completed\r\n")); err != nil {
			done <- err
			return
		}
		store, err = read()
		if err != nil {
			done <- err
			return
		}
	}
	if _, err := conn.Write([]byte(imapTag(store) + " OK STORE completed\r\n")); err != nil {
		done <- err
		return
	}
	expunge, err := read()
	if err != nil {
		done <- err
		return
	}
	if _, err := conn.Write([]byte("* 1 EXPUNGE\r\n" + imapTag(expunge) + " OK UID EXPUNGE completed\r\n")); err != nil {
		done <- err
		return
	}
	commands <- seen
	done <- nil
}

func serveLeadingSpaceScopedOperation(ln net.Listener, serverTLS *tls.Config, operation string, done chan<- error) {
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
	write := func(response string) bool {
		if _, writeErr := conn.Write([]byte(response)); writeErr != nil {
			done <- writeErr
			return false
		}
		return true
	}
	read := func(want string) (string, bool) {
		line, readErr := reader.ReadString('\n')
		if readErr != nil {
			done <- readErr
			return "", false
		}
		if !strings.Contains(strings.ToUpper(line), want) {
			done <- fmt.Errorf("received %q, want %s", line, want)
			return "", false
		}
		return line, true
	}

	if !write("* OK [CAPABILITY IMAP4rev1 UIDPLUS] ready\r\n") {
		return
	}
	login, ok := read("LOGIN")
	if !ok || !write(imapTag(login)+" OK LOGIN completed\r\n") {
		return
	}
	selectCmd, ok := read("INBOX")
	if !ok || !write("* 1 EXISTS\r\n"+imapTag(selectCmd)+" OK SELECT completed\r\n") {
		return
	}
	if operation == "list" {
		search, ok := read("UID SEARCH")
		if !ok || !write("* SEARCH 42\r\n"+imapTag(search)+" OK SEARCH completed\r\n") {
			return
		}
	}

	fetch, ok := read("UID FETCH 42")
	if !ok {
		return
	}
	envelope := "ENVELOPE (\"03-Oct-2026 02:00:00 +0000\" \"subject\" ((NIL NIL \"sender\" \"example.com\")) NIL NIL ((NIL NIL \" alias\" \"icloud.com\")) NIL NIL NIL NIL)"
	response := "* 1 FETCH (UID 42 " + envelope + ")\r\n"
	if operation != "delete" {
		body := "From: sender@example.com\r\n" +
			"To: \" alias\"@icloud.com\r\n" +
			"Subject: leading space local-part\r\n" +
			"Content-Type: text/plain; charset=utf-8\r\n\r\nbody"
		bodyItem := "BODY[]"
		if operation == "detail" {
			bodyItem = "RFC822"
		}
		response = fmt.Sprintf("* 1 FETCH (UID 42 %s INTERNALDATE \"03-Oct-2026 02:00:00 +0000\" %s {%d}\r\n%s)\r\n", envelope, bodyItem, len(body), body)
	}
	if !write(response + imapTag(fetch) + " OK FETCH completed\r\n") {
		return
	}
	// Keep the server connection alive until the operation has consumed the
	// tagged FETCH response. For delete, also prove rejection happened before
	// any STORE/EXPUNGE mutation command.
	if err := conn.SetReadDeadline(time.Now().Add(time.Second)); err != nil {
		done <- err
		return
	}
	line, readErr := reader.ReadString('\n')
	if operation == "delete" {
		if line != "" {
			done <- fmt.Errorf("unauthorized delete sent mutation command %q", line)
			return
		}
		if readErr == nil {
			done <- fmt.Errorf("expected client connection to close after rejection")
			return
		}
	}
	done <- nil
}

func useIMAPTimeouts(t *testing.T, connect, command time.Duration) {
	t.Helper()
	oldConnect, oldCommand := imapConnectTimeout, imapCommandTimeout
	imapConnectTimeout, imapCommandTimeout = connect, command
	t.Cleanup(func() {
		imapConnectTimeout, imapCommandTimeout = oldConnect, oldCommand
	})
}

func newTestTLSListener(t *testing.T) (net.Listener, *tls.Config, *tls.Config) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, publicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(cert)
	serverTLS := &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: privateKey}},
		MinVersion:   tls.VersionTLS12,
	}
	clientTLS := &tls.Config{
		RootCAs:    roots,
		ServerName: "127.0.0.1",
		MinVersion: tls.VersionTLS12,
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return ln, serverTLS, clientTLS
}

func newTestClient(t *testing.T, ln net.Listener, tlsConfig *tls.Config) *Client {
	t.Helper()
	host, portText, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	c := NewClientWithServer("user@example.com", "secret", host, port)
	c.tlsConfig = tlsConfig
	return c
}

func imapTag(line string) string {
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}
