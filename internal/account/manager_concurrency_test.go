package account

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"icloud-hme/internal/hme"
	"icloud-hme/internal/mail"
)

func TestManagerRemoveAccountRestoresMemoryWhenSaveFails(t *testing.T) {
	dir := t.TempDir()
	m, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	accountID := addMailTestAccount(t, m, "keep", "keep@icloud.com", "password")
	pool := newControlledMailPool()
	m.imapPool = pool

	notDirectory := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(notDirectory, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	m.dataDir = notDirectory

	removed, err := m.RemoveAccount(accountID)
	if removed {
		t.Fatal("remove reported success after persistence failure")
	}
	var persistenceErr *PersistenceError
	if !errors.As(err, &persistenceErr) {
		t.Fatalf("remove error=%v, want PersistenceError", err)
	}
	if _, ok := m.GetAccount(accountID); !ok {
		t.Fatal("account was not restored in memory after persistence failure")
	}
	reloaded, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Close()
	if _, ok := reloaded.GetAccount(accountID); !ok {
		t.Fatal("persisted account was lost after failed removal")
	}
	_, drops, _ := pool.snapshot()
	if len(drops) != 0 {
		t.Fatalf("mail pool drops=%v, want none", drops)
	}
}

type recordedMailCall struct {
	key      string
	username string
	password string
	server   string
	port     int
}

type controlledMailPool struct {
	mu sync.Mutex

	calls  []recordedMailCall
	drops  []string
	closes int

	doStarted   chan string
	doBlock     map[string]<-chan struct{}
	dropStarted chan string
	dropBlock   map[string]<-chan struct{}
}

func newControlledMailPool() *controlledMailPool {
	return &controlledMailPool{
		doStarted:   make(chan string, 8),
		doBlock:     make(map[string]<-chan struct{}),
		dropStarted: make(chan string, 8),
		dropBlock:   make(map[string]<-chan struct{}),
	}
}

func (p *controlledMailPool) DoConfig(key, username, password, server string, port int, fn func(*mail.Client) error) error {
	p.mu.Lock()
	p.calls = append(p.calls, recordedMailCall{
		key: key, username: username, password: password, server: server, port: port,
	})
	block := p.doBlock[key]
	p.mu.Unlock()
	p.doStarted <- key
	if block != nil {
		<-block
	}
	return fn(nil)
}

func (p *controlledMailPool) Drop(key string) {
	p.mu.Lock()
	p.drops = append(p.drops, key)
	block := p.dropBlock[key]
	p.mu.Unlock()
	p.dropStarted <- key
	if block != nil {
		<-block
	}
}

func (p *controlledMailPool) Close() {
	p.mu.Lock()
	p.closes++
	p.mu.Unlock()
}

func (p *controlledMailPool) snapshot() ([]recordedMailCall, []string, int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]recordedMailCall(nil), p.calls...), append([]string(nil), p.drops...), p.closes
}

func addMailTestAccount(t *testing.T, m *Manager, name, email, password string) string {
	t.Helper()
	summary, err := m.AddAccountWithInput(AddAccountInput{Name: name, ICloudEmail: email})
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.accounts[summary.ID].AppPassword = password
	err = m.save()
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	return summary.ID
}

func waitForKey(t *testing.T, ch <-chan string, want string) {
	t.Helper()
	select {
	case got := <-ch:
		if got != want {
			t.Fatalf("event key=%q, want %q", got, want)
		}
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for event %q", want)
	}
}

func TestManagerSlowMailRequestDoesNotBlockOtherAccountUpdate(t *testing.T) {
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	accountA := addMailTestAccount(t, m, "A", "a@icloud.com", "password-a")
	accountB := addMailTestAccount(t, m, "B", "b@icloud.com", "password-b")

	pool := newControlledMailPool()
	releaseA := make(chan struct{})
	pool.doBlock[accountA] = releaseA
	m.imapPool = pool
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(releaseA) }) })

	requestDone := make(chan error, 1)
	go func() {
		requestDone <- m.WithMailClient(accountA, func(*mail.Client) error { return nil })
	}()
	waitForKey(t, pool.doStarted, accountA)

	newEmail := "new-b@icloud.com"
	updateDone := make(chan error, 1)
	go func() {
		_, err := m.UpdateMetadata(accountB, UpdateAccountInput{ICloudEmail: &newEmail})
		updateDone <- err
	}()
	select {
	case err := <-updateDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("slow account-a mail request blocked account-b update")
	}

	releaseOnce.Do(func() { close(releaseA) })
	if err := <-requestDone; err != nil {
		t.Fatal(err)
	}
}

func TestManagerMailConfigUpdateWaitsThroughPoolDrop(t *testing.T) {
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	accountID := addMailTestAccount(t, m, "A", "old@icloud.com", "password-a")

	pool := newControlledMailPool()
	releaseRequest := make(chan struct{})
	releaseDrop := make(chan struct{})
	pool.doBlock[accountID] = releaseRequest
	pool.dropBlock[accountID] = releaseDrop
	m.imapPool = pool
	var requestOnce sync.Once
	var dropOnce sync.Once
	t.Cleanup(func() {
		requestOnce.Do(func() { close(releaseRequest) })
		dropOnce.Do(func() { close(releaseDrop) })
	})

	oldRequestDone := make(chan error, 1)
	go func() {
		oldRequestDone <- m.WithMailClient(accountID, func(*mail.Client) error { return nil })
	}()
	waitForKey(t, pool.doStarted, accountID)

	newEmail := "new@icloud.com"
	updateDone := make(chan error, 1)
	go func() {
		_, err := m.UpdateMetadata(accountID, UpdateAccountInput{ICloudEmail: &newEmail})
		updateDone <- err
	}()
	select {
	case err := <-updateDone:
		t.Fatalf("same-account update completed before old request: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	requestOnce.Do(func() { close(releaseRequest) })
	if err := <-oldRequestDone; err != nil {
		t.Fatal(err)
	}
	waitForKey(t, pool.dropStarted, accountID)

	newRequestDone := make(chan error, 1)
	go func() {
		newRequestDone <- m.WithMailClient(accountID, func(*mail.Client) error { return nil })
	}()
	select {
	case key := <-pool.doStarted:
		t.Fatalf("new request reached pool before old config was dropped: %q", key)
	case <-time.After(50 * time.Millisecond):
	}

	dropOnce.Do(func() { close(releaseDrop) })
	if err := <-updateDone; err != nil {
		t.Fatal(err)
	}
	waitForKey(t, pool.doStarted, accountID)
	if err := <-newRequestDone; err != nil {
		t.Fatal(err)
	}

	calls, drops, _ := pool.snapshot()
	if len(calls) != 2 || calls[0].username != "old@icloud.com" || calls[1].username != newEmail {
		t.Fatalf("mail calls did not transition atomically from old to new config: %+v", calls)
	}
	if len(drops) != 1 || drops[0] != accountID {
		t.Fatalf("pool drops=%v, want [%s]", drops, accountID)
	}
}

func TestManagerReloadWaitsForMailRequestBeforeClosingPool(t *testing.T) {
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	accountID := addMailTestAccount(t, m, "A", "a@icloud.com", "password-a")

	pool := newControlledMailPool()
	releaseRequest := make(chan struct{})
	pool.doBlock[accountID] = releaseRequest
	m.imapPool = pool
	var releaseOnce sync.Once
	t.Cleanup(func() { releaseOnce.Do(func() { close(releaseRequest) }) })

	requestDone := make(chan error, 1)
	go func() {
		requestDone <- m.WithMailClient(accountID, func(*mail.Client) error { return nil })
	}()
	waitForKey(t, pool.doStarted, accountID)

	reloadDone := make(chan error, 1)
	go func() { reloadDone <- m.Reload() }()
	select {
	case err := <-reloadDone:
		t.Fatalf("reload completed while old mail request was active: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	releaseOnce.Do(func() { close(releaseRequest) })
	if err := <-requestDone; err != nil {
		t.Fatal(err)
	}
	if err := <-reloadDone; err != nil {
		t.Fatal(err)
	}
	_, _, closes := pool.snapshot()
	if closes != 1 {
		t.Fatalf("pool close count=%d, want 1", closes)
	}
}

func writeAccountSnapshotAtomically(t *testing.T, path string, accounts map[string]*Account) {
	t.Helper()
	raw, err := json.Marshal(struct {
		Accounts map[string]*Account `json:"accounts"`
	}{Accounts: accounts})
	if err != nil {
		t.Fatal(err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".external-accounts-*.tmp")
	if err != nil {
		t.Fatal(err)
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if err := tmp.Chmod(0600); err != nil {
		tmp.Close()
		t.Fatal(err)
	}
	if _, err := tmp.Write(raw); err != nil {
		tmp.Close()
		t.Fatal(err)
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		t.Fatal(err)
	}
	if err := tmp.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		t.Fatal(err)
	}
}

func addHMETestAccount(t *testing.T, m *Manager, name string, cookies map[string]string) string {
	t.Helper()
	summary, err := m.AddAccountWithInput(AddAccountInput{Name: name, ICloudEmail: name + "@icloud.com"})
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	m.accounts[summary.ID].Cookies = cloneCookies(cookies)
	err = m.save()
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	return summary.ID
}

func TestManagerCookieUpdateWaitsForOldHMEWriteback(t *testing.T) {
	dir := t.TempDir()
	m, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	accountID := addHMETestAccount(t, m, "a", map[string]string{"session": "old"})

	operationStarted := make(chan struct{})
	releaseOperation := make(chan struct{})
	operationDone := make(chan error, 1)
	go func() {
		operationDone <- m.WithHMEClient(accountID, false, func(client *hme.Client) error {
			close(operationStarted)
			<-releaseOperation
			client.Cookies = map[string]string{"session": "old-refreshed"}
			return nil
		})
	}()
	<-operationStarted

	validationStarted := make(chan string, 1)
	m.cookieValidator = func(snap *Account) (*hme.Client, error) {
		validationStarted <- snap.Cookies["session"]
		return &hme.Client{Cookies: map[string]string{"session": "new-refreshed"}}, nil
	}
	updateDone := make(chan error, 1)
	go func() {
		updateDone <- m.UpdateCookies(accountID, map[string]string{"session": "new"})
	}()
	select {
	case cookie := <-validationStarted:
		t.Fatalf("cookie update began before the old HME operation completed: %q", cookie)
	case <-time.After(50 * time.Millisecond):
	}

	close(releaseOperation)
	if err := <-operationDone; err != nil {
		t.Fatal(err)
	}
	select {
	case cookie := <-validationStarted:
		if cookie != "new" {
			t.Fatalf("validator received cookie %q, want new", cookie)
		}
	case <-time.After(time.Second):
		t.Fatal("cookie update did not start after the old HME operation completed")
	}
	if err := <-updateDone; err != nil {
		t.Fatal(err)
	}

	got, ok := m.GetAccount(accountID)
	if !ok {
		t.Fatal("account disappeared")
	}
	if got.Cookies["session"] != "new-refreshed" {
		t.Fatalf("final cookie=%q, want new-refreshed", got.Cookies["session"])
	}
	reloaded, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	persisted, ok := reloaded.GetAccount(accountID)
	if !ok || persisted.Cookies["session"] != "new-refreshed" {
		t.Fatalf("persisted cookie=%q, want new-refreshed", persisted.Cookies["session"])
	}
}

func TestManagerReloadPreservesSnapshotReadBeforeOldHMEWriteback(t *testing.T) {
	dir := t.TempDir()
	m, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	accountID := addHMETestAccount(t, m, "old", map[string]string{"session": "old"})

	operationStarted := make(chan struct{})
	releaseOperation := make(chan struct{})
	operationDone := make(chan error, 1)
	go func() {
		operationDone <- m.WithHMEClient(accountID, false, func(client *hme.Client) error {
			close(operationStarted)
			<-releaseOperation
			client.Cookies = map[string]string{"session": "old-refreshed"}
			return nil
		})
	}()
	<-operationStarted

	external, ok := m.GetAccount(accountID)
	if !ok {
		t.Fatal("account disappeared before external write")
	}
	external.Name = "external"
	external.Cookies = map[string]string{"session": "external"}
	writeAccountSnapshotAtomically(t, m.dataFile, map[string]*Account{accountID: external})

	snapshotRead := make(chan struct{})
	m.reloadSnapshotRead = func() { close(snapshotRead) }
	reloadDone := make(chan error, 1)
	go func() { reloadDone <- m.Reload() }()
	select {
	case <-snapshotRead:
	case <-time.After(time.Second):
		t.Fatal("reload did not read the external snapshot while HME was active")
	}
	select {
	case err := <-reloadDone:
		t.Fatalf("reload completed before the old HME operation: %v", err)
	default:
	}

	close(releaseOperation)
	if err := <-operationDone; err != nil {
		t.Fatal(err)
	}
	if err := <-reloadDone; err != nil {
		t.Fatal(err)
	}

	got, ok := m.GetAccount(accountID)
	if !ok || got.Name != "external" || got.Cookies["session"] != "external" {
		t.Fatalf("memory did not retain external snapshot: account=%+v ok=%v", got, ok)
	}
	reloaded, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Close()
	persisted, ok := reloaded.GetAccount(accountID)
	if !ok || persisted.Name != "external" || persisted.Cookies["session"] != "external" {
		t.Fatalf("disk did not retain external snapshot: account=%+v ok=%v", persisted, ok)
	}
}

func TestManagerReloadSerializesConcurrentAccountAddAfterSnapshotRead(t *testing.T) {
	dir := t.TempDir()
	m, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	accountID := addHMETestAccount(t, m, "old", map[string]string{"session": "old"})

	operationStarted := make(chan struct{})
	releaseOperation := make(chan struct{})
	operationDone := make(chan error, 1)
	go func() {
		operationDone <- m.WithHMEClient(accountID, false, func(client *hme.Client) error {
			close(operationStarted)
			<-releaseOperation
			client.Cookies = map[string]string{"session": "old-refreshed"}
			return nil
		})
	}()
	<-operationStarted

	external, ok := m.GetAccount(accountID)
	if !ok {
		t.Fatal("account disappeared before external write")
	}
	external.Name = "external"
	external.Cookies = map[string]string{"session": "external"}
	writeAccountSnapshotAtomically(t, m.dataFile, map[string]*Account{accountID: external})

	snapshotRead := make(chan struct{})
	m.reloadSnapshotRead = func() { close(snapshotRead) }
	reloadDone := make(chan error, 1)
	go func() { reloadDone <- m.Reload() }()
	select {
	case <-snapshotRead:
	case <-time.After(time.Second):
		t.Fatal("reload did not read the external snapshot while HME was active")
	}

	type addResult struct {
		summary Summary
		err     error
	}
	addDone := make(chan addResult, 1)
	go func() {
		summary, err := m.AddAccountWithInput(AddAccountInput{
			Name:        "added",
			ICloudEmail: "added@icloud.com",
		})
		addDone <- addResult{summary: summary, err: err}
	}()
	select {
	case result := <-addDone:
		t.Fatalf("account add committed before reload completed: %+v", result)
	case <-time.After(50 * time.Millisecond):
	}

	close(releaseOperation)
	if err := <-operationDone; err != nil {
		t.Fatal(err)
	}
	if err := <-reloadDone; err != nil {
		t.Fatal(err)
	}
	result := <-addDone
	if result.err != nil {
		t.Fatal(result.err)
	}

	gotExternal, ok := m.GetAccount(accountID)
	if !ok || gotExternal.Name != "external" || gotExternal.Cookies["session"] != "external" {
		t.Fatalf("memory did not retain external snapshot: account=%+v ok=%v", gotExternal, ok)
	}
	gotAdded, ok := m.GetAccount(result.summary.ID)
	if !ok || gotAdded.Name != "added" {
		t.Fatalf("memory did not retain post-reload add: account=%+v ok=%v", gotAdded, ok)
	}

	reloaded, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Close()
	persistedExternal, ok := reloaded.GetAccount(accountID)
	if !ok || persistedExternal.Name != "external" || persistedExternal.Cookies["session"] != "external" {
		t.Fatalf("disk did not retain external snapshot: account=%+v ok=%v", persistedExternal, ok)
	}
	persistedAdded, ok := reloaded.GetAccount(result.summary.ID)
	if !ok || persistedAdded.Name != "added" {
		t.Fatalf("disk did not retain post-reload add: account=%+v ok=%v", persistedAdded, ok)
	}
}

func TestManagerReloadWritebackFailureLeavesMemoryAndPoolUntouched(t *testing.T) {
	dir := t.TempDir()
	m, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	accountID := addHMETestAccount(t, m, "old", map[string]string{"session": "old"})
	pool := newControlledMailPool()
	m.imapPool = pool

	external, ok := m.GetAccount(accountID)
	if !ok {
		t.Fatal("account disappeared before external write")
	}
	external.Name = "external"
	writeAccountSnapshotAtomically(t, m.dataFile, map[string]*Account{accountID: external})
	notDirectory := filepath.Join(dir, "not-a-directory")
	if err := os.WriteFile(notDirectory, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	m.reloadSnapshotRead = func() { m.dataDir = notDirectory }

	if err := m.Reload(); err == nil {
		t.Fatal("reload succeeded despite snapshot writeback failure")
	}
	got, ok := m.GetAccount(accountID)
	if !ok || got.Name != "old" || got.Cookies["session"] != "old" {
		t.Fatalf("failed reload changed memory: account=%+v ok=%v", got, ok)
	}
	_, _, closes := pool.snapshot()
	if closes != 0 {
		t.Fatalf("failed reload closed pool %d times", closes)
	}
}

func TestManagerSlowHMEOperationDoesNotBlockOtherAccount(t *testing.T) {
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	accountA := addHMETestAccount(t, m, "a", map[string]string{"session": "a"})
	accountB := addHMETestAccount(t, m, "b", map[string]string{"session": "b"})

	operationStarted := make(chan struct{})
	releaseOperation := make(chan struct{})
	operationADone := make(chan error, 1)
	go func() {
		operationADone <- m.WithHMEClient(accountA, false, func(*hme.Client) error {
			close(operationStarted)
			<-releaseOperation
			return nil
		})
	}()
	<-operationStarted

	operationBDone := make(chan error, 1)
	go func() {
		operationBDone <- m.WithHMEClient(accountB, false, func(*hme.Client) error { return nil })
	}()
	select {
	case err := <-operationBDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("slow account-a HME operation blocked account-b")
	}

	close(releaseOperation)
	if err := <-operationADone; err != nil {
		t.Fatal(err)
	}
}
