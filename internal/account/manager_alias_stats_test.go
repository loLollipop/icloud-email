package account

import (
	"errors"
	"testing"
	"time"

	"icloud-hme/internal/hme"
)

func setStoredAliasStats(t *testing.T, m *Manager, id string, active, total int) {
	t.Helper()
	m.mu.Lock()
	m.accounts[id].AliasActive = active
	m.accounts[id].AliasTotal = total
	err := m.save()
	m.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
}

func TestManagerListAliasesSynchronizesAndPersistsAuthoritativeStats(t *testing.T) {
	dir := t.TempDir()
	m, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	id := addHMETestAccount(t, m, "stats", map[string]string{"session": "old"})
	setStoredAliasStats(t, m, id, 63, 63)

	aliases := make([]hme.Alias, 68)
	for i := range aliases {
		aliases[i].Active = i < 65
	}
	m.aliasLister = func(client *hme.Client) ([]hme.Alias, error) {
		client.Cookies = map[string]string{"session": "refreshed"}
		return aliases, nil
	}

	got, err := m.ListAliases(id, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 68 {
		t.Fatalf("ListAliases returned %d aliases, want 68", len(got))
	}
	stored, _ := m.GetAccount(id)
	if stored.AliasActive != 65 || stored.AliasTotal != 68 {
		t.Fatalf("memory stats=%d/%d, want 65/68", stored.AliasActive, stored.AliasTotal)
	}
	if stored.Cookies["session"] != "refreshed" {
		t.Fatalf("memory cookie=%q, want refreshed", stored.Cookies["session"])
	}

	reloaded, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Close()
	persisted, _ := reloaded.GetAccount(id)
	if persisted.AliasActive != 65 || persisted.AliasTotal != 68 {
		t.Fatalf("persisted stats=%d/%d, want 65/68", persisted.AliasActive, persisted.AliasTotal)
	}
	if persisted.Cookies["session"] != "refreshed" {
		t.Fatalf("persisted cookie=%q, want refreshed", persisted.Cookies["session"])
	}
}

func TestManagerListAliasesPreservesGoodStatsOnFailureAndAcceptsEmptyList(t *testing.T) {
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := addHMETestAccount(t, m, "stats", map[string]string{"session": "old"})
	setStoredAliasStats(t, m, id, 60, 68)

	m.aliasLister = func(*hme.Client) ([]hme.Alias, error) {
		return nil, errors.New("upstream unavailable")
	}
	if _, err := m.ListAliases(id, false); err == nil {
		t.Fatal("ListAliases succeeded, want upstream error")
	}
	stored, _ := m.GetAccount(id)
	if stored.AliasActive != 60 || stored.AliasTotal != 68 {
		t.Fatalf("failed fetch replaced stats with %d/%d", stored.AliasActive, stored.AliasTotal)
	}

	m.aliasLister = func(*hme.Client) ([]hme.Alias, error) { return []hme.Alias{}, nil }
	if _, err := m.ListAliases(id, false); err != nil {
		t.Fatal(err)
	}
	stored, _ = m.GetAccount(id)
	if stored.AliasActive != 0 || stored.AliasTotal != 0 {
		t.Fatalf("empty authoritative list left stats=%d/%d, want 0/0", stored.AliasActive, stored.AliasTotal)
	}
}

func TestManagerAliasMutationRefreshFailureDoesNotFailMutation(t *testing.T) {
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := addHMETestAccount(t, m, "stats", map[string]string{"session": "old"})
	setStoredAliasStats(t, m, id, 60, 68)
	m.aliasLister = func(*hme.Client) ([]hme.Alias, error) {
		return nil, errors.New("refresh failed")
	}

	called := false
	err = m.WithHMEClientAndAliasRefresh(id, false, func(client *hme.Client) error {
		called = true
		client.Cookies = map[string]string{"session": "mutation-refreshed"}
		return nil
	})
	if err != nil {
		t.Fatalf("successful mutation failed due to auxiliary refresh: %v", err)
	}
	if !called {
		t.Fatal("mutation callback was not called")
	}
	stored, _ := m.GetAccount(id)
	if stored.AliasActive != 60 || stored.AliasTotal != 68 {
		t.Fatalf("failed refresh replaced stats with %d/%d", stored.AliasActive, stored.AliasTotal)
	}
	if stored.Cookies["session"] != "mutation-refreshed" {
		t.Fatalf("mutation cookie=%q, want mutation-refreshed", stored.Cookies["session"])
	}
}

func TestManagerAliasRefreshSerializesAgainstStaleListWriteback(t *testing.T) {
	m, err := NewManager(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	id := addHMETestAccount(t, m, "stats", map[string]string{"session": "old"})
	setStoredAliasStats(t, m, id, 63, 63)

	firstStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	listCalls := 0
	m.aliasLister = func(*hme.Client) ([]hme.Alias, error) {
		listCalls++
		if listCalls == 1 {
			close(firstStarted)
			<-releaseFirst
			return make([]hme.Alias, 68), nil
		}
		aliases := make([]hme.Alias, 69)
		for i := range aliases {
			aliases[i].Active = true
		}
		return aliases, nil
	}

	listDone := make(chan error, 1)
	go func() {
		_, err := m.ListAliases(id, false)
		listDone <- err
	}()
	<-firstStarted
	mutationStarted := make(chan struct{})
	mutationDone := make(chan error, 1)
	go func() {
		mutationDone <- m.WithHMEClientAndAliasRefresh(id, false, func(*hme.Client) error {
			close(mutationStarted)
			return nil
		})
	}()
	select {
	case <-mutationStarted:
		t.Fatal("mutation started before the older list operation released the account lock")
	case <-time.After(30 * time.Millisecond):
	}
	close(releaseFirst)
	if err := <-listDone; err != nil {
		t.Fatal(err)
	}
	if err := <-mutationDone; err != nil {
		t.Fatal(err)
	}
	stored, _ := m.GetAccount(id)
	if stored.AliasActive != 69 || stored.AliasTotal != 69 {
		t.Fatalf("stale list won writeback: stats=%d/%d, want 69/69", stored.AliasActive, stored.AliasTotal)
	}
}
