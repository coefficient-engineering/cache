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

This invalidates matching entries in local L1 and L2. With a backplane configured, it also publishes one tag-level notification so receivers can evict matching entries using their own L1 tag indexes, including entries never cached by the sender. Remote eviction happens only when the message is delivered and processed; the call is not a cross-node acknowledgement.

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
3. Publishes one `MessageTypeDeleteByTags` backplane message with `Tags: []string{tag}` and an empty `Key`, even if no local keys matched or the L2 operation failed, unless backplane notifications are skipped.

`DeleteByTags` follows the same steps, publishing one message containing its deduplicated tags rather than one message per tag or key. For L2 it calls `l2.TagsDeleter.DeleteByTags(ctx, tags)` when the adapter implements that optional interface, and otherwise `l2.Adapter.DeleteByTag` once per tag. Either way the L2 work runs under one `DistributedCacheHardTimeout` and counts as one call for the L2 circuit breaker. The Redis adapter implements `TagsDeleter` through control-hash generation updates; there is no one-round-trip guarantee across Cluster routing or recovery. Pipelining alone would not provide whole-batch atomicity.

### L1 consistency and failures

Tag invalidation hard-evicts matching L1 entries, including stale/fail-safe copies; they cannot subsequently be used as fail-safe fallbacks. Receivers only process messages whose `CacheName` and `KeyPrefix` both exactly match their configuration. This backplane isolation does not isolate L2 tag metadata: configure a separate L2 adapter namespace for independent caches.

Delivery is best-effort and asynchronous, even when publishing is synchronous. Duplicate or delayed messages can evict fresh matching entries. Concurrent writes and in-flight factory results may survive invalidation or be evicted; there is no global generation or factory-start fence. Redis pub/sub does not replay messages lost during a disconnect, and reconnecting alone does not clear L1. See [Backplane and Multi-Node Caching](../explanation/BackplaneAndMultiNode.md#delivery-and-concurrent-writes) for recovery and receipt-event semantics.

Existing error and skip policies still apply. L2 invalidation failures are logged and only returned when configured to rethrow; notifications are still attempted after an L2 error. Skipping L2 writes or failing to invalidate L2 can leave data that refills an evicted L1 entry. `SkipBackplaneNotifications` (also enabled by `WithSkipL2`) prevents the remote notification. Rethrown L2 and backplane errors are joined; neither rolls back local eviction.

For Redis, successful adapter invalidation fences matching Sets completed before invalidation began from later L2 reads. An overlapping Set may survive or immediately miss; this is not factory-start ordering. Metadata loss fails closed and automatically recovers under a fresh incarnation, not reset generations. An ambiguous Redis error may mean an update was applied; the core's default L2 error policy can swallow adapter errors, so enable rethrowing when you need an acknowledgement. Redis rollback/restore of acknowledged history is outside this guarantee.

Redis physical cleanup requires an application-owned, cancellable `RunMaintenance(ctx)` loop. Valid `TTL=0` entries remain indefinite; invalidated, never-read entries are eventually reclaimed while repeated primary sweeps succeed. The new versioned layout is a cold cutover and does not share invalidations with legacy adapters. See [Adding a Distributed L2 Cache](AddingDistributedL2.md#redis-invalidation-and-lifecycle) for lifecycle and client capability requirements.

Fallback failure reports show the first three failed tags and count the remaining failures. All underlying errors, including those omitted from the text, remain available through `errors.Is` and `errors.As`.

### Compatibility

Existing `l2.Adapter` implementations need no changes: `l2.TagsDeleter` is an optional batching capability. Adapters without it retain their existing `DeleteByTag` behavior, including any lack of tag support. Custom implementations and generated mocks of the `Cache` interface must add the new `DeleteByTags` method.

Backplane participants and custom transports require a coordinated upgrade for `MessageTypeDeleteByTags` and the new `Tags`/`KeyPrefix` fields. Older nodes ignore type 5; upgraded nodes with nonempty prefixes reject legacy messages missing `KeyPrefix`. There is no per-key fallback or mixed-version guarantee. See [Writing Backplane Adapters](WritingBackplaneAdapters.md#coordinated-upgrades).

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
