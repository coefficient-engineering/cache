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
3. **`Set` must preserve tag associations** if you want `DeleteByTag` to work. The cache passes the tags slice to `Set`. You may use a reverse index or store generation snapshots with each record.

## Method contracts

### `Get(ctx context.Context, key string) ([]byte, error)`

Retrieve the opaque bytes stored under `key`. Return `(nil, nil)` on a miss, including a logically invalidated record. Backend failures must remain errors, not masquerade as successful validation.

### `Set(ctx context.Context, key string, value []byte, ttl time.Duration, tags []string) error`

Store raw bytes under `key` with the given TTL. A TTL of 0 means the entry does not expire. `tags` associates string labels with this key for bulk invalidation. A nil or empty tags slice means no tag associations.

Overwrites must replace the old tag associations. With a reverse index, remove obsolete memberships; with generation snapshots, replace the record's snapshots.

### `Delete(ctx context.Context, key string) error`

Remove the key. Must not return an error if the key does not exist. Also clean up reverse-index associations if your implementation maintains them.

### `DeleteByTag(ctx context.Context, tag string) error`

Invalidate all keys stored with the given tag. Physical deletion may be deferred if `Get` rejects invalidated records and the implementation provides its documented reclamation lifecycle. If tagging is not needed, implement this as a no-op returning `nil`.

### Optional: `DeleteByTags(ctx context.Context, tags []string) error`

Implement `l2.TagsDeleter` to invalidate the keys of several tags in one call, allowing backend batching. This does not promise one network round trip or whole-batch atomicity: document your implementation's ordering and partial-failure behavior. `Cache.DeleteByTags` uses it when your adapter implements it, and otherwise calls `DeleteByTag` once per tag. The tags it receives are never empty and hold no duplicates.

Without it, the fallback calls `DeleteByTag` for each tag in turn, all under one `DistributedCacheHardTimeout`. A tag that fails doesn't stop the others. Failure reports name the first three failed tags and count the remaining errors; all causes remain available through `errors.Is` and `errors.As`. Once the context is done, the remaining tags are skipped and the context error is included once. Each tag still invokes your adapter separately, so implement `TagsDeleter` to batch backend calls.

The interface is optional to preserve source compatibility with existing L2 adapters, mocks, and wrappers. A wrapper embedding `l2.Adapter` must explicitly implement `DeleteByTags` to preserve batching. A wrapper embedding a concrete adapter that implements `TagsDeleter` inherits that method: override it too if your `DeleteByTag` override applies tag rewriting, access checks, or other behavior that bulk deletion must also enforce.

### `Clear(ctx context.Context) error`

Invalidate all entries managed by this adapter. Implementations should scope this to a configured namespace to avoid accidentally wiping shared data. As with tag invalidation, logical invalidation may precede physical reclamation.

### `Ping(ctx context.Context) error`

Check that the backend is reachable. Used by health checks and auto-recovery logic.

## Tag tracking strategies

The adapter is responsible for preserving tag associations. Common approaches:

**Redis (built-in adapter)**: Store an opaque payload plus incarnation/Clear/tag-generation snapshots in each record. Validate against one authoritative primary control hash on `Get`; advance generations to invalidate without cross-slot payload scripts. Payloads stay sharded, but metadata traffic concentrates on one shard. Missing control state recovers with a fresh incarnation through non-replayed initialization; missing generations fail closed. Application-owned `RunMaintenance(ctx)` reclaims invalid records, including indefinite-TTL records. See [Redis configuration, lifecycle and migration](AddingDistributedL2.md#redis-invalidation-and-lifecycle) for client capability requirements, cold cutover and the Redis history-rollback exclusion.

A reverse-index scheme using a Redis SET per tag and a companion SET per key is another possible design, but scripts cannot discover and access arbitrary cross-slot keys in Redis Cluster. Pipelining such scripts does not fix that restriction or make the batch atomic.

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
- `adapters/l2/redis/redis.go` for Redis generation-based invalidation; its constructor returns `(*Adapter, error)` and requires compatible primary-access/collection capabilities

## See also

- [Adding a Distributed L2 Cache](AddingDistributedL2.md) for wiring an L2 adapter
- [The Adapter Pattern](../explanation/TheAdapterPattern.md) for why the library uses interfaces
