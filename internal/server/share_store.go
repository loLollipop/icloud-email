package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"icloud-hme/internal/mail"
)

type shareGrant struct {
	AccountID string             `json:"account_id"`
	AliasID   string             `json:"alias_id"`
	Email     string             `json:"email"`
	TokenHash string             `json:"token_hash"`
	Boundary  mail.ShareBoundary `json:"boundary"`
	inFlight  int
	// delivery serializes response commitment with removal/rotation of this
	// grant. Never wait for it while holding shareStore.mu.
	delivery sync.Mutex
}

type shareStore struct {
	mu     sync.Mutex
	grants map[string]*shareGrant
	file   string
	err    error
}

func shareKey(accountID, aliasID string) string {
	raw, _ := json.Marshal([]string{accountID, aliasID})
	return string(raw)
}

func tokenHash(token string) string {
	digest := sha256.Sum256([]byte(token))
	return hex.EncodeToString(digest[:])
}

func newShareStore(dir string) (*shareStore, error) {
	s := &shareStore{grants: make(map[string]*shareGrant)}
	if dir == "" {
		return s, nil
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	s.file = filepath.Join(dir, "shares.json")
	raw, err := os.ReadFile(s.file)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(s.file, 0600); err != nil {
		return nil, err
	}
	var disk struct {
		Grants []*shareGrant `json:"grants"`
	}
	if err := json.Unmarshal(raw, &disk); err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	for _, grant := range disk.Grants {
		if grant == nil || grant.AccountID == "" || grant.AliasID == "" || grant.Email == "" || grant.Boundary.MinUID == 0 || grant.Boundary.UIDValidity == 0 || grant.Boundary.CreatedAt.IsZero() || grant.Boundary.MailboxIdentity == "" {
			return nil, fmt.Errorf("invalid share store")
		}
		digest, err := hex.DecodeString(grant.TokenHash)
		if err != nil || len(digest) != sha256.Size || seen[grant.TokenHash] {
			return nil, fmt.Errorf("invalid share digest")
		}
		key := shareKey(grant.AccountID, grant.AliasID)
		if s.grants[key] != nil {
			return nil, fmt.Errorf("duplicate share grant")
		}
		seen[grant.TokenHash] = true
		s.grants[key] = grant
	}
	return s, nil
}

// saveLocked syncs the private temporary file before atomically replacing it.
func (s *shareStore) saveLocked() error {
	if s.file == "" {
		return nil
	}
	disk := struct {
		Grants []*shareGrant `json:"grants"`
	}{Grants: []*shareGrant{}}
	for _, grant := range s.grants {
		disk.Grants = append(disk.Grants, grant)
	}
	raw, err := json.Marshal(disk)
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.file)
	tmp, err := os.CreateTemp(dir, ".shares-*.tmp")
	if err != nil {
		return err
	}
	defer func() { _ = tmp.Close(); _ = os.Remove(tmp.Name()) }()
	if err = tmp.Chmod(0600); err != nil {
		return err
	}
	if _, err = tmp.Write(raw); err != nil {
		return err
	}
	if err = tmp.Sync(); err != nil {
		return err
	}
	if err = tmp.Close(); err != nil {
		return err
	}
	if err = os.Rename(tmp.Name(), s.file); err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		folder, err := os.Open(dir)
		if err != nil {
			return err
		}
		err = folder.Sync()
		_ = folder.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

func (s *shareStore) currentLocked(grant *shareGrant) bool {
	return s.err == nil && s.grants[shareKey(grant.AccountID, grant.AliasID)] == grant
}

// lockCurrent takes the current grant's delivery lock, then the store lock.
// A rotation between pointer lookup and locking is retried. The returned
// unlock releases both; callers must release the store before any network I/O.
func (s *shareStore) lockCurrent(accountID, aliasID string) (*shareGrant, func()) {
	key := shareKey(accountID, aliasID)
	for {
		s.mu.Lock()
		grant := s.grants[key]
		s.mu.Unlock()
		if grant != nil {
			grant.delivery.Lock()
		}
		s.mu.Lock()
		if s.grants[key] == grant {
			return grant, func() {
				s.mu.Unlock()
				if grant != nil {
					grant.delivery.Unlock()
				}
			}
		}
		s.mu.Unlock()
		if grant != nil {
			grant.delivery.Unlock()
		}
	}
}

func shareCreatedAt(grant *shareGrant) string {
	return grant.Boundary.CreatedAt.UTC().Format(time.RFC3339)
}
