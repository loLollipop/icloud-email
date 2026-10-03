package mail

import (
	"bufio"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	imapclient "github.com/emersion/go-imap/client"
)

func TestPoolLockOrCreateDoesNotBlockOtherAccounts(t *testing.T) {
	p := NewPool()
	accountA := p.lockOrCreate("account-a", "a@example.com", "secret-a", "imap.example.com", 993)

	waitingOnA := make(chan struct{})
	waiterDone := make(chan struct{})
	var signalOnce sync.Once
	p.afterLookup = func(key string) {
		if key == "account-a" {
			signalOnce.Do(func() { close(waitingOnA) })
		}
	}
	go func() {
		pc := p.lockOrCreate("account-a", "a@example.com", "secret-a", "imap.example.com", 993)
		pc.mu.Unlock()
		close(waiterDone)
	}()

	select {
	case <-waitingOnA:
	case <-time.After(time.Second):
		accountA.mu.Unlock()
		t.Fatal("second account-a request did not reach the busy account slot")
	}

	accountBDone := make(chan struct{})
	go func() {
		pc := p.lockOrCreate("account-b", "b@example.com", "secret-b", "imap.example.com", 993)
		pc.mu.Unlock()
		close(accountBDone)
	}()
	select {
	case <-accountBDone:
	case <-time.After(time.Second):
		accountA.mu.Unlock()
		t.Fatal("busy account-a slot blocked account-b pool lookup")
	}

	accountA.mu.Unlock()
	select {
	case <-waiterDone:
	case <-time.After(time.Second):
		t.Fatal("queued account-a request did not resume")
	}
}

func TestPoolExternalConfigUsesStableKeyAndReplacesChangedConfig(t *testing.T) {
	p := NewPool()
	first := p.lockOrCreate("acc_1", "user@gmail.com", "old", "imap.gmail.com", 993)
	first.mu.Unlock()
	again := p.lockOrCreate("acc_1", "user@gmail.com", "old", "imap.gmail.com", 993)
	again.mu.Unlock()
	if first != again || len(p.items) != 1 {
		t.Fatal("unchanged external mailbox config should reuse one pooled slot")
	}

	changed := p.lockOrCreate("acc_1", "user@gmail.com", "new", "imap.gmail.com", 993)
	changed.mu.Unlock()
	if changed != first || changed.password != "new" || len(p.items) != 1 {
		t.Fatal("changed credentials should replace config in the same stable slot")
	}

	reconfigured := p.lockOrCreate("acc_1", "other@gmail.com", "new", "imap.example.com", 1993)
	reconfigured.mu.Unlock()
	if first.username != "other@gmail.com" || first.server != "imap.example.com" || first.port != 1993 {
		t.Fatal("username/server changes were not applied")
	}
	p.Drop("acc_1")
	if len(p.items) != 0 {
		t.Fatal("Drop should remove the account slot")
	}
}

func TestPoolDefaultAndExternalKeysDoNotCollide(t *testing.T) {
	p := NewPool()
	defaultConn := p.lockOrCreate("icloud:same@example.com", "same@example.com", "one", IMAPServer, IMAPPort)
	defaultConn.mu.Unlock()
	externalConn := p.lockOrCreate("acc_1", "same@example.com", "two", "imap.gmail.com", 993)
	externalConn.mu.Unlock()
	if len(p.items) != 2 {
		t.Fatalf("default and external configurations collided: %d slots", len(p.items))
	}
}

func TestPoolCloseTerminatesConnectionWithoutLogout(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	observed := make(chan string, 1)
	serverDone := make(chan struct{})
	var releaseOnce sync.Once
	stopServer := func() { releaseOnce.Do(func() { close(release) }) }
	t.Cleanup(func() {
		stopServer()
		_ = ln.Close()
		select {
		case <-serverDone:
		case <-time.After(time.Second):
			t.Error("pool close test server did not stop")
		}
	})

	go func() {
		defer close(serverDone)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		if _, err := conn.Write([]byte("* OK [CAPABILITY IMAP4rev1] ready\r\n")); err != nil {
			return
		}
		line, readErr := bufio.NewReader(conn).ReadString('\n')
		observed <- line
		if readErr == nil {
			<-release // A graceful LOGOUT would block here waiting for a response.
		}
	}()

	cli, err := imapclient.DialWithDialer(&net.Dialer{Timeout: time.Second}, ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	cli.Timeout = 5 * time.Second
	c := &Client{cli: cli}
	p := NewPool()
	p.items["account"] = &pooledConn{client: c}

	closeDone := make(chan struct{})
	go func() {
		p.Close()
		close(closeDone)
	}()

	var line string
	select {
	case line = <-observed:
	case <-time.After(time.Second):
		stopServer()
		t.Fatal("server did not observe the connection closing")
	}
	if strings.Contains(strings.ToUpper(line), "LOGOUT") {
		stopServer()
		t.Fatalf("Pool.Close sent a graceful LOGOUT: %q", line)
	}
	select {
	case <-closeDone:
	case <-time.After(500 * time.Millisecond):
		stopServer()
		<-closeDone
		t.Fatal("Pool.Close blocked on an unresponsive server")
	}
	if c.cli != nil {
		t.Fatal("Pool.Close left the client attached")
	}
	if len(p.items) != 0 {
		t.Fatalf("Pool.Close retained %d entries", len(p.items))
	}
	stopServer()
}
