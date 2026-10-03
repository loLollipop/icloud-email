package server

import (
	"errors"
	"sync"
	"time"

	"icloud-hme/internal/hme"
)

var errAliasLoadPanicked = errors.New("alias load panicked")

type aliasCacheEntry struct {
	aliases   []hme.Alias
	expiresAt time.Time
}

type aliasFlight struct {
	done    chan struct{}
	aliases []hme.Alias
	err     error
}

// aliasCache is a short-lived, per-account cache with same-key request coalescing.
type aliasCache struct {
	mu      sync.Mutex
	ttl     time.Duration
	now     func() time.Time
	entries map[string]aliasCacheEntry
	flights map[string]*aliasFlight
}

func newAliasCache(ttl time.Duration) *aliasCache {
	return &aliasCache{
		ttl:     ttl,
		now:     time.Now,
		entries: make(map[string]aliasCacheEntry),
		flights: make(map[string]*aliasFlight),
	}
}

func cloneAliases(in []hme.Alias) []hme.Alias {
	if in == nil {
		return nil
	}
	return append([]hme.Alias(nil), in...)
}

func (c *aliasCache) get(accountID string, load func() ([]hme.Alias, error)) ([]hme.Alias, error) {
	c.mu.Lock()
	if entry, ok := c.entries[accountID]; ok && c.now().Before(entry.expiresAt) {
		aliases := cloneAliases(entry.aliases)
		c.mu.Unlock()
		return aliases, nil
	}
	if flight, ok := c.flights[accountID]; ok {
		c.mu.Unlock()
		<-flight.done
		return cloneAliases(flight.aliases), flight.err
	}
	flight := &aliasFlight{done: make(chan struct{})}
	c.flights[accountID] = flight
	c.mu.Unlock()

	var aliases []hme.Alias
	var err error
	var panicValue any
	func() {
		defer func() {
			panicValue = recover()
		}()
		aliases, err = load()
	}()
	if panicValue != nil {
		c.mu.Lock()
		if c.flights[accountID] == flight {
			delete(c.flights, accountID)
		}
		flight.err = errAliasLoadPanicked
		close(flight.done)
		c.mu.Unlock()
		panic(panicValue)
	}
	c.mu.Lock()
	// An invalidation removes this exact flight, preventing a request that
	// started before a mutation from repopulating stale data.
	if c.flights[accountID] == flight {
		delete(c.flights, accountID)
		if err == nil {
			flight.aliases = cloneAliases(aliases)
			c.entries[accountID] = aliasCacheEntry{
				aliases:   cloneAliases(aliases),
				expiresAt: c.now().Add(c.ttl),
			}
		}
	}
	flight.err = err
	if flight.aliases == nil && err == nil {
		flight.aliases = cloneAliases(aliases)
	}
	close(flight.done)
	c.mu.Unlock()
	return cloneAliases(aliases), err
}

func (c *aliasCache) invalidate(accountID string) {
	c.mu.Lock()
	delete(c.entries, accountID)
	delete(c.flights, accountID)
	c.mu.Unlock()
}

func (c *aliasCache) invalidateAll() {
	c.mu.Lock()
	clear(c.entries)
	clear(c.flights)
	c.mu.Unlock()
}
