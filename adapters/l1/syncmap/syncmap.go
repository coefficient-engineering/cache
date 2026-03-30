// Package syncmap provides an unbounded [l1.Adapter] backed by [sync.Map].
//
// This is the default L1 adapter used when no custom adapter is passed
// to [cache.New]. It offers excellent performance under read-heavy
// workloads due to sync.Map's lock striping, but provides no eviction,
// no size limits, and no cost awareness.
//
// # Characteristics
//
//   - Unbounded — no eviction, no size limits.
//   - Ignores the cost parameter in [Adapter.Set].
//   - [Adapter.Clear] iterates and deletes all entries (no O(1) clear).
//   - [Adapter.Close] is a no-op.
//
// For production services with memory constraints, consider a bounded
// adapter backed by Ristretto, Theine, or Otter.
package syncmap

import (
	"sync"

	"github.com/coefficient-engineering/cache/l1"
)

// Adapter is an unbounded L1 cache backed by [sync.Map].
//
// Create one with [New]:
//
//	adapter := syncmap.New()
//
// This is the default L1 adapter used when no custom adapter is configured.
// It offers excellent performance under read-heavy workloads
// but provides no eviction, no size limits, and no cost awareness.
//
// For production services with memory constraints, consider a bounded
// adapter backed by Ristretto, Theine, or Otter.
type Adapter struct {
	m sync.Map
}

// New returns a new [sync.Map]-backed L1 adapter.
func New() *Adapter {
	return &Adapter{}
}

func (a *Adapter) Get(key string) (any, bool) {
	return a.m.Load(key)
}

func (a *Adapter) Set(key string, value any, cost int64) {
	// sync.Map is unbounded, cost is intentionally ignored.
	a.m.Store(key, value)
}

func (a *Adapter) Delete(key string) {
	a.m.Delete(key)
}

func (a *Adapter) LoadAndDelete(key string) (any, bool) {
	return a.m.LoadAndDelete(key)
}

func (a *Adapter) CompareAndSwap(key string, old, new any) bool {
	return a.m.CompareAndSwap(key, old, new)
}

func (a *Adapter) Range(fn func(key string, value any) bool) {
	a.m.Range(func(k, v any) bool {
		return fn(k.(string), v)
	})
}

func (a *Adapter) Clear() {
	a.m.Range(func(key, _ any) bool {
		a.m.Delete(key)
		return true
	})
}

func (a *Adapter) Close() error {
	// sync.Map has no resources to release.
	return nil
}

var _ l1.Adapter = (*Adapter)(nil)
