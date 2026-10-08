package account

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"icloud-hme/internal/mail"
)

func TestMailboxVersionCannotReviveAfterTargetRestoration(t *testing.T) {
	dir := t.TempDir()
	mgr, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	summary, err := mgr.AddAccountWithInput(AddAccountInput{Name: "share", ICloudEmail: "first@icloud.com"})
	if err != nil {
		t.Fatal(err)
	}
	original, _ := mgr.GetAccount(summary.ID)
	secondEmail := "second@icloud.com"
	if _, err := mgr.UpdateMetadata(summary.ID, UpdateAccountInput{ICloudEmail: &secondEmail}); err != nil {
		t.Fatal(err)
	}
	changed, _ := mgr.GetAccount(summary.ID)
	firstEmail := "first@icloud.com"
	if _, err := mgr.UpdateMetadata(summary.ID, UpdateAccountInput{ICloudEmail: &firstEmail}); err != nil {
		t.Fatal(err)
	}
	restored, _ := mgr.GetAccount(summary.ID)
	if changed.MailboxVersion == original.MailboxVersion || restored.MailboxVersion == original.MailboxVersion || restored.MailboxVersion == changed.MailboxVersion {
		t.Fatal("A -> B -> A restored old mailbox generation")
	}
	restarted, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer restarted.Close()
	afterRestart, _ := restarted.GetAccount(summary.ID)
	if afterRestart.MailboxVersion != restored.MailboxVersion {
		t.Fatal("mailbox generation not persisted")
	}
	name := "renamed"
	if _, err := mgr.UpdateMetadata(summary.ID, UpdateAccountInput{Name: &name}); err != nil {
		t.Fatal(err)
	}
	renamed, _ := mgr.GetAccount(summary.ID)
	if renamed.MailboxVersion != restored.MailboxVersion {
		t.Fatal("unrelated name update revoked mailbox generation")
	}
}

func TestMailboxConfigurationCommitOrdersConcurrentUpdateAndRemoval(t *testing.T) {
	for _, mutation := range []string{"update", "remove", "reload"} {
		t.Run(mutation, func(t *testing.T) {
			mgr, err := NewManager(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			defer mgr.Close()
			summary, err := mgr.AddAccountWithInput(AddAccountInput{Name: "shared", ICloudEmail: "first@icloud.com"})
			if err != nil {
				t.Fatal(err)
			}
			acc, _ := mgr.GetAccount(summary.ID)
			checked, release := make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			defer releaseOnce.Do(func() { close(release) })
			committed := make(chan error, 1)
			go func() {
				committed <- mgr.WithMailboxConfiguration(summary.ID, acc.MailboxVersion, func() error { close(checked); <-release; return nil })
			}()
			select {
			case <-checked:
			case <-time.After(time.Second):
				t.Fatal("commit did not validate")
			}
			started, changed := make(chan struct{}), make(chan error, 1)
			go func() {
				close(started)
				var err error
				switch mutation {
				case "update":
					email := "second@icloud.com"
					_, err = mgr.UpdateMetadata(summary.ID, UpdateAccountInput{ICloudEmail: &email})
				case "remove":
					_, err = mgr.RemoveAccount(summary.ID)
				case "reload":
					err = mgr.Reload()
				}
				changed <- err
			}()
			<-started
			select {
			case err := <-changed:
				t.Fatalf("configuration mutation completed during old delivery: %v", err)
			case <-time.After(30 * time.Millisecond):
			}
			releaseOnce.Do(func() { close(release) })
			select {
			case err := <-committed:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("commit blocked")
			}
			select {
			case err := <-changed:
				if err != nil {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("mutation blocked after commit")
			}
			called := false
			err = mgr.WithMailboxConfiguration(summary.ID, acc.MailboxVersion, func() error { called = true; return nil })
			if mutation != "reload" && (called || !errors.Is(err, mail.ErrShareIdentityChanged)) {
				t.Fatalf("old configuration callback ran after mutation: called=%v err=%v", called, err)
			}
		})
	}
}

func TestReloadCannotRestoreHistoricalMailboxGeneration(t *testing.T) {
	dir := t.TempDir()
	mgr, err := NewManager(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	summary, err := mgr.AddAccountWithInput(AddAccountInput{Name: "share", ICloudEmail: "first@icloud.com"})
	if err != nil {
		t.Fatal(err)
	}
	original, _ := mgr.GetAccount(summary.ID)
	secondEmail := "second@icloud.com"
	if _, err := mgr.UpdateMetadata(summary.ID, UpdateAccountInput{ICloudEmail: &secondEmail}); err != nil {
		t.Fatal(err)
	}
	changed, _ := mgr.GetAccount(summary.ID)
	// Simulate an operator restoring old on-disk configuration, including its
	// historical version. Reload must generate a fresh version before commit.
	raw, err := json.Marshal(struct {
		Accounts map[string]*Account `json:"accounts"`
	}{map[string]*Account{summary.ID: original}})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "accounts.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err := mgr.Reload(); err != nil {
		t.Fatal(err)
	}
	restored, _ := mgr.GetAccount(summary.ID)
	if restored.MailboxVersion == original.MailboxVersion || restored.MailboxVersion == changed.MailboxVersion {
		t.Fatal("reload revived historical generation")
	}
	if err := mgr.Reload(); err != nil {
		t.Fatal(err)
	}
	unchanged, _ := mgr.GetAccount(summary.ID)
	if unchanged.MailboxVersion != restored.MailboxVersion {
		t.Fatal("unchanged reload unexpectedly changed generation")
	}
}
