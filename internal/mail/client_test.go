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
