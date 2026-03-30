# Writing L1 Adapters

The `l1.Adapter` interface defines the contract for the in-process memory cache layer. The default adapter uses `sync.Map`, which is unbounded and ignores cost. To use a bounded cache (Ristretto, Theine, Otter, or similar), implement this interface.

## The interface

```go
package l1

type Adapter interface {
    Get(key string) (any, bool)
    Set(key string, value any, cost int64)
    Delete(key string)
    LoadAndDelete(key string) (any, bool)
    Range(fn func(key string, value any) bool)
    CompareAndSwap(key string, old, new any) bool
    Clear()
    Close() error
}
```

## Method contracts

### `Get(key string) (any, bool)`

Return the stored value and `true` if the key exists. Return `(nil, false)` on a miss. Never return an error.

### `Set(key string, value any, cost int64)`

Store the value under the key, overwriting any existing entry.

`cost` is an advisory hint representing the entry's relative weight. Bounded caches use this for eviction decisions. Unbounded implementations may ignore it. The cache passes the `Size` field from `EntryOptions` as the cost.

### `Delete(key string)`

Remove the entry for the key. Must not panic if the key does not exist.

### `LoadAndDelete(key string) (any, bool)`

Atomically retrieve and remove the entry. Return `(value, true)` if the key was present, `(nil, false)` otherwise. The cache uses this in `Delete` and `DeleteByTag` to clean up tag associations.

### `Range(fn func(key string, value any) bool)`

Call `fn` for every entry in the cache. If `fn` returns `false`, stop iteration. The iteration order is not guaranteed.

`fn` must not call other methods on the `Adapter` from within the callback, as this may deadlock depending on the implementation.

### `CompareAndSwap(key string, old, new any) bool`

Atomically replace the value for `key` only if the current value is identical (`==`) to `old`. Return `true` if the swap was performed. The cache uses this in `Expire` to update an entry in-place without racing with a concurrent `Set`.

### `Clear()`

Remove all entries from the cache.

### `Close() error`

Release any resources held by the adapter (background goroutines, metric buffers, etc.). Return `nil` if there is nothing to clean up.

## Example: wrapping a bounded cache

This skeleton shows the structure for wrapping a hypothetical bounded cache library:

```go
package mybounded

import (
    "github.com/coefficient-engineering/cache/l1"
    "github.com/example/boundedcache"
)

type Adapter struct {
    inner *boundedcache.Cache
}

func New(maxSize int64) *Adapter {
    return &Adapter{
        inner: boundedcache.New(maxSize),
    }
}

func (a *Adapter) Get(key string) (any, bool) {
    return a.inner.Get(key)
}

func (a *Adapter) Set(key string, value any, cost int64) {
    a.inner.Set(key, value, cost)
}

func (a *Adapter) Delete(key string) {
    a.inner.Delete(key)
}

func (a *Adapter) LoadAndDelete(key string) (any, bool) {
    return a.inner.GetAndDelete(key)
}

func (a *Adapter) Range(fn func(key string, value any) bool) {
    a.inner.ForEach(func(k string, v any) bool {
        return fn(k, v)
    })
}

func (a *Adapter) CompareAndSwap(key string, old, new any) bool {
    return a.inner.CompareAndSwap(key, old, new)
}

func (a *Adapter) Clear() {
    a.inner.Purge()
}

func (a *Adapter) Close() error {
    a.inner.Close()
    return nil
}

// Compile-time check.
var _ l1.Adapter = (*Adapter)(nil)
```

## Concurrency

All methods must be safe for concurrent use. The cache calls L1 methods from multiple goroutines concurrently.

## Using your adapter

```go
c, err := cache.New(
    cache.WithL1(mybounded.New(10_000)),
)
```

## Reference implementation

The default `sync.Map` adapter is at `adapters/l1/syncmap/syncmap.go`. It is a minimal, correct implementation to use as a template.
