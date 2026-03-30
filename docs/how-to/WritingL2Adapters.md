# Writing L2 Adapters

The `l2.Adapter` interface defines the contract for any distributed cache backend. The cache calls these methods with already-serialized `[]byte` payloads. The adapter stores and retrieves opaque bytes without needing to understand Go types.

## The interface

```go
package l2

type Adapter interface {
    Get(ctx context.Context, key string) ([]byte, error)
    Set(ctx context.Context, key string, value []byte, ttl time.Duration, tags []string) error
    Delete(ctx context.Context, key string) error
    DeleteByTag(ctx context.Context, tag string) error
    Clear(ctx context.Context) error
    Ping(ctx context.Context) error
}
```

## Three rules

These three rules are mandatory for correct operation:

1. **`Get` must return `(nil, nil)` on a cache miss.** Do not return an error for a missing key. Only return a non-nil error for genuine I/O failures.
2. **`Delete` must not return an error if the key does not exist.** Deletes are idempotent.
3. **`Set` must track tag associations** if you want `DeleteByTag` to work. The cache passes the tags slice to `Set`. How you store the tag-to-key mapping is up to you.

## Method contracts

### `Get(ctx context.Context, key string) ([]byte, error)`

Retrieve the raw bytes stored under `key`. Return `(nil, nil)` on a miss. Return a non-nil error only for I/O failures.

### `Set(ctx context.Context, key string, value []byte, ttl time.Duration, tags []string) error`

Store raw bytes under `key` with the given TTL. A TTL of 0 means the entry does not expire. `tags` associates string labels with this key for bulk invalidation. A nil or empty tags slice means no tag associations.

If overwriting an existing entry, clean up any old tag associations before storing the new ones.

### `Delete(ctx context.Context, key string) error`

Remove the key. Must not return an error if the key does not exist. Also clean up any tag associations for the deleted key.

### `DeleteByTag(ctx context.Context, tag string) error`

Remove all keys that were stored with the given tag. How tag-to-key associations are tracked is entirely up to the implementation. If tagging is not needed, implement this as a no-op returning `nil`.

### `Clear(ctx context.Context) error`

Remove all entries managed by this adapter. Implementations should scope this to a configured key prefix to avoid accidentally wiping a shared namespace.

### `Ping(ctx context.Context) error`

Check that the backend is reachable. Used by health checks and auto-recovery logic.

## Tag tracking strategies

The adapter is responsible for maintaining tag-to-key associations. Common approaches:

**Redis**: Use a SET per tag (`tag:{tagname}`) containing the keys. Use a companion SET per key (`tags:{key}`) containing the tag names. On `DeleteByTag`, read the tag SET, delete each key, then delete the tag SET.

**In-memory**: Use a `map[string]map[string]struct{}` (tag to set of keys). The built-in `adapters/l2/memory` adapter does this with `sync.Map`.

**Database (e.g., DynamoDB)**: Use a secondary index on the tag field, or store tags in a separate table.

If your backend does not support tagging and you do not need `DeleteByTag`, implement it as:

```go
func (a *Adapter) DeleteByTag(ctx context.Context, tag string) error {
    return nil // no-op
}
```

## Example: wrapping a hypothetical cache client

```go
package mycache

import (
    "context"
    "errors"
    "time"

    "github.com/coefficient-engineering/cache/l2"
    "github.com/example/somecache"
)

type Adapter struct {
    client    *somecache.Client
    keyPrefix string
    tags      map[string]map[string]struct{} // tag -> set of keys (simplified)
}

func New(client *somecache.Client, keyPrefix string) *Adapter {
    return &Adapter{
        client:    client,
        keyPrefix: keyPrefix,
        tags:      make(map[string]map[string]struct{}),
    }
}

func (a *Adapter) Get(ctx context.Context, key string) ([]byte, error) {
    data, err := a.client.Get(ctx, a.keyPrefix+key)
    if errors.Is(err, somecache.ErrNotFound) {
        return nil, nil // miss, not an error
    }
    return data, err
}

func (a *Adapter) Set(ctx context.Context, key string, value []byte, ttl time.Duration, tags []string) error {
    pk := a.keyPrefix + key
    if err := a.client.Set(ctx, pk, value, ttl); err != nil {
        return err
    }
    for _, tag := range tags {
        if a.tags[tag] == nil {
            a.tags[tag] = make(map[string]struct{})
        }
        a.tags[tag][pk] = struct{}{}
    }
    return nil
}

func (a *Adapter) Delete(ctx context.Context, key string) error {
    pk := a.keyPrefix + key
    err := a.client.Delete(ctx, pk)
    if errors.Is(err, somecache.ErrNotFound) {
        return nil // missing key is not an error
    }
    return err
}

func (a *Adapter) DeleteByTag(ctx context.Context, tag string) error {
    keys := a.tags[tag]
    for key := range keys {
        _ = a.client.Delete(ctx, key)
    }
    delete(a.tags, tag)
    return nil
}

func (a *Adapter) Clear(ctx context.Context) error {
    return a.client.DeleteByPrefix(ctx, a.keyPrefix)
}

func (a *Adapter) Ping(ctx context.Context) error {
    return a.client.Ping(ctx)
}

// Compile-time check.
var _ l2.Adapter = (*Adapter)(nil)
```

## Concurrency

All methods must be safe for concurrent use. The cache calls L2 methods from multiple goroutines. If your internal state (like a tag map) is not thread-safe, protect it with a mutex.

## Reference implementations

- `adapters/l2/memory/memory.go` for a simple in-process implementation
- `adapters/l2/redis/redis.go` for a production Redis implementation with Lua-based tag cleanup

## See also

- [Adding a Distributed L2 Cache](AddingDistributedL2.md) for wiring an L2 adapter
- [The Adapter Pattern](../explanation/TheAdapterPattern.md) for why the library uses interfaces
