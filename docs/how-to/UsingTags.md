# Using Tags

Tags allow you to associate string labels with cache entries and invalidate all entries sharing a tag in a single call.

## Tag entries

Pass `WithTags` when storing a value:

```go
value, err := cache.GetOrSet(ctx, c, "product:42", factory,
    cache.WithTags("category:electronics", "featured"),
)

value2, err := cache.GetOrSet(ctx, c, "product:99", factory,
    cache.WithTags("category:electronics"),
)
```

Both entries now carry the tag `"category:electronics"`. Entry `"product:42"` also carries `"featured"`.

Tags are additive. Calling `WithTags` multiple times in the same operation appends to the list:

```go
cache.WithTags("a"), cache.WithTags("b") // entry gets both tags "a" and "b"
```

## Invalidate by tag

```go
err := c.DeleteByTag(ctx, "category:electronics")
```

This invalidates matching entries in local L1 and L2. Backplane messages cover the keys known to the invalidating node's L1 tag index; they do **not** guarantee eviction of remote-only L1 copies. That limitation is tracked in [issue #38](https://github.com/coefficient-engineering/cache/issues/38).

To invalidate several tags at once, use `DeleteByTags`:

```go
err := c.DeleteByTags(ctx, []string{"category:electronics", "featured"})
```

It invalidates every entry carrying any of the tags, as if `DeleteByTag` were called for each one. Duplicate tags are ignored, and an empty slice is a no-op. L2 work shares one hard timeout and one circuit breaker record; backend round trips and atomicity depend on the adapter.

## How tag tracking works

**L1 (in-process)**: The cache maintains an in-memory reverse index mapping each tag to the set of prefixed keys that carry it. This is a `map[string]map[string]struct{}` protected by a `sync.RWMutex`.

**L2 (distributed)**: Tag associations are the responsibility of the L2 adapter. The `l2.Adapter.Set` method receives the tags alongside the key and value. Storage and invalidation are implementation-specific:

- The in-memory L2 adapter uses a `sync.Map` of tag to key sets.
- The Redis L2 adapter stores generation snapshots with each payload and checks authoritative primary control metadata on reads. Tag invalidation advances generations; it does not enumerate or physically delete every matching payload. Payloads remain spread across Cluster shards, while one shared control hash is a metadata hotspot.

When `DeleteByTag` is called, the cache:

1. Retrieves all L1 keys for the tag from the reverse index and deletes them.
2. Calls `l2.Adapter.DeleteByTag(ctx, tag)`, which handles L2 invalidation.
3. Publishes a `MessageTypeDelete` backplane message for each removed key.

`DeleteByTags` follows the same steps for all its tags. For L2 it calls `l2.TagsDeleter.DeleteByTags(ctx, tags)` when the adapter implements that optional interface, and otherwise `l2.Adapter.DeleteByTag` once per tag. Either way the L2 work runs under one `DistributedCacheHardTimeout` and counts as one call for the L2 circuit breaker. The Redis adapter implements `TagsDeleter` through control-hash generation updates; there is no one-round-trip guarantee across Cluster routing or recovery. Pipelining alone would not provide whole-batch atomicity.

For Redis, successful adapter invalidation fences matching Sets completed before invalidation began from later L2 reads. An overlapping Set may survive or immediately miss; this is not factory-start ordering. Metadata loss fails closed and automatically recovers under a fresh incarnation, not reset generations. An ambiguous Redis error may mean an update was applied; the core's default L2 error policy can swallow adapter errors, so enable rethrowing when you need an acknowledgement. Redis rollback/restore of acknowledged history is outside this guarantee.

Redis physical cleanup requires an application-owned, cancellable `RunMaintenance(ctx)` loop. Valid `TTL=0` entries remain indefinite; invalidated, never-read entries are eventually reclaimed while repeated primary sweeps succeed. The new versioned layout is a cold cutover and does not share invalidations with legacy adapters. See [Adding a Distributed L2 Cache](AddingDistributedL2.md#redis-invalidation-and-lifecycle) for lifecycle and client capability requirements.

Fallback failure reports show the first three failed tags and count the remaining failures. All underlying errors, including those omitted from the text, remain available through `errors.Is` and `errors.As`.

### Compatibility

Existing `l2.Adapter` implementations need no changes: `l2.TagsDeleter` is an optional batching capability. Adapters without it retain their existing `DeleteByTag` behavior, including any lack of tag support. Custom implementations and generated mocks of the `Cache` interface must add the new `DeleteByTags` method.

## Tags as a default

You can set tags at the cache level so all entries receive them:

```go
c, err := cache.New(
    cache.WithDefaultEntryOptions(cache.EntryOptions{
        Tags: []string{"service:products"},
    }),
)
```

Per-call `WithTags` appends to (not replaces) the defaults.
