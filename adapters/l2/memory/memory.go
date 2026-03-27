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

// Adapter is an in-process L2 cache backed by sync.Map.
type Adapter struct {
	store sync.Map
	tags  sync.Map // tag → map[string]struct{} (set of keys)
}

func New() *Adapter {
	return &Adapter{}
}

func (a *Adapter) Get(_ context.Context, key string) ([]byte, error) {
	raw, ok := a.store.Load(key)
	if !ok {
		return nil, nil // miss, NOT an error
	}
	e := raw.(*entry)
	if !e.expiresAt.IsZero() && time.Now().After(e.expiresAt) {
		a.store.Delete(key)
		return nil, nil // expired = miss
	}
	return e.data, nil
}

func (a *Adapter) Set(_ context.Context, key string, value []byte, ttl time.Duration) error {
	e := &entry{
		data: make([]byte, len(value)),
	}
	copy(e.data, value) // defensive copy

	if ttl > 0 {
		e.expiresAt = time.Now().Add(ttl)
	}

	a.store.Store(key, e)
	return nil
}

// SetWithTags stores value with tags. This is a memory-adapter-specific
// extension for testing tag support.
func (a *Adapter) SetWithTags(_ context.Context, key string, value []byte, ttl time.Duration, tags []string) error {
	e := &entry{
		data: make([]byte, len(value)),
		tags: tags,
	}
	copy(e.data, value)
	if ttl > 0 {
		e.expiresAt = time.Now().Add(ttl)
	}
	a.store.Store(key, e)

	// Track tag → key associations
	for _, tag := range tags {
		raw, _ := a.tags.LoadOrStore(tag, &sync.Map{})
		tagKeys := raw.(*sync.Map)
		tagKeys.Store(key, struct{}{})
	}

	return nil
}

func (a *Adapter) Delete(_ context.Context, key string) error {
	a.store.Delete(key)
	return nil // NOT an error if key doesn't exist
}

func (a *Adapter) DeleteByTag(_ context.Context, tag string) error {
	raw, ok := a.tags.Load(tag)
	if !ok {
		return nil
	}
	tagKeys := raw.(*sync.Map)
	tagKeys.Range(func(key, _ any) bool {
		a.store.Delete(key.(string))
		return true
	})
	a.tags.Delete(tag)
	return nil
}

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

func (a *Adapter) Ping(_ context.Context) error {
	return nil // always healthy
}

var _ l2.Adapter = (*Adapter)(nil)
