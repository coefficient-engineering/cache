// Package memory provides an in-process [l2.Adapter] backed by [sync.Map].
//
// This adapter is intended for testing multi-node scenarios in a single
// process. It is NOT suitable for production distributed caching.
//
// # Characteristics
//
//   - Stores []byte data with optional TTL. Expired entries are lazily
//     removed on [Adapter.Get].
//   - Tracks tag-to-key associations using a [sync.Map] of [sync.Map]s.
//   - [Adapter.Clear] iterates and deletes all entries and tag associations.
//   - [Adapter.Ping] always returns nil.
package memory

import (
	"context"
	"sync"
	"time"

	"github.com/coefficient-engineering/cache/l2"
)

type entry struct {
	data      []byte
	expiresAt time.Time
	tags      []string
}

// Adapter is an in-process L2 cache backed by [sync.Map].
//
// Create one with [New]:
//
//	adapter := memory.New()
//
// Intended for testing multi-node scenarios in a single process.
type Adapter struct {
	store sync.Map
	tags  sync.Map // tag → map[string]struct{} (set of keys)
}

// New returns a new in-process L2 adapter.
func New() *Adapter {
	return &Adapter{}
}

// loadEntry returns the entry stored under key. A value of an unexpected
// type cannot occur (only *entry is stored) and is treated as a miss.
func (a *Adapter) loadEntry(key string) (*entry, bool) {
	raw, ok := a.store.Load(key)
	if !ok {
		return nil, false
	}
	e, ok := raw.(*entry)
	return e, ok
}

// loadTagKeys returns the key set tracked for tag. A value of an unexpected
// type cannot occur (only [sync.Map] pointers are stored) and is treated as
// absent.
func (a *Adapter) loadTagKeys(tag string) (*sync.Map, bool) {
	raw, ok := a.tags.Load(tag)
	if !ok {
		return nil, false
	}
	tagKeys, ok := raw.(*sync.Map)
	return tagKeys, ok
}

// untag removes key from the key sets of every tag in tags.
func (a *Adapter) untag(key string, tags []string) {
	for _, tag := range tags {
		if tagKeys, ok := a.loadTagKeys(tag); ok {
			tagKeys.Delete(key)
		}
	}
}

// Get returns the bytes stored under key, or (nil, nil) on a miss.
// Expired entries are removed lazily and reported as a miss.
func (a *Adapter) Get(_ context.Context, key string) ([]byte, error) {
	e, ok := a.loadEntry(key)
	if !ok {
		return nil, nil // miss, NOT an error
	}
	if !e.expiresAt.IsZero() && time.Now().After(e.expiresAt) {
		a.store.Delete(key)
		return nil, nil // expired = miss
	}
	return e.data, nil
}

// Set stores a copy of value under key with the given TTL (0 means no
// expiry) and replaces any previous tag associations for key with tags.
func (a *Adapter) Set(_ context.Context, key string, value []byte, ttl time.Duration, tags []string) error {
	// Clean old tag associations if overwriting an existing entry.
	if old, ok := a.loadEntry(key); ok {
		a.untag(key, old.tags)
	}

	// Defensive copy of data and tags.
	tagsCopy := make([]string, len(tags))
	copy(tagsCopy, tags)

	e := &entry{
		data: make([]byte, len(value)),
		tags: tagsCopy,
	}
	copy(e.data, value)

	if ttl > 0 {
		e.expiresAt = time.Now().Add(ttl)
	}

	a.store.Store(key, e)

	// Track tag → key associations.
	for _, tag := range tagsCopy {
		raw, _ := a.tags.LoadOrStore(tag, &sync.Map{})
		if tagKeys, ok := raw.(*sync.Map); ok {
			tagKeys.Store(key, struct{}{})
		}
	}

	return nil
}

// Delete removes key and its tag associations. A missing key is not an
// error.
func (a *Adapter) Delete(_ context.Context, key string) error {
	if raw, loaded := a.store.LoadAndDelete(key); loaded {
		if e, ok := raw.(*entry); ok {
			a.untag(key, e.tags)
		}
	}
	return nil // NOT an error if key doesn't exist
}

// DeleteByTag removes every key associated with tag, then the tag itself.
func (a *Adapter) DeleteByTag(_ context.Context, tag string) error {
	tagKeys, ok := a.loadTagKeys(tag)
	if !ok {
		return nil
	}
	tagKeys.Range(func(key, _ any) bool {
		a.store.Delete(key)
		return true
	})
	a.tags.Delete(tag)
	return nil
}

// Clear removes all entries and tag associations.
func (a *Adapter) Clear(_ context.Context) error {
	a.store.Range(func(key, _ any) bool {
		a.store.Delete(key)
		return true
	})
	a.tags.Range(func(key, _ any) bool {
		a.tags.Delete(key)
		return true
	})
	return nil
}

// Ping always returns nil; the in-process store is always reachable.
func (*Adapter) Ping(_ context.Context) error {
	return nil
}

var _ l2.Adapter = (*Adapter)(nil)
