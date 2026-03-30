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

This removes all entries tagged with `"category:electronics"` from both L1 and L2, and notifies the backplane (if configured) so other nodes evict their copies too.

## How tag tracking works

**L1 (in-process)**: The cache maintains an in-memory reverse index mapping each tag to the set of prefixed keys that carry it. This is a `map[string]map[string]struct{}` protected by a `sync.RWMutex`.

**L2 (distributed)**: Tag-to-key associations are the responsibility of the L2 adapter. The `l2.Adapter.Set` method receives the tags alongside the key and value. How the adapter stores this mapping is implementation-specific:

- The in-memory L2 adapter uses a `sync.Map` of tag to key sets.
- The Redis L2 adapter uses a Redis SET per tag (`tag:{tagname}`) and a companion SET per key (`tags:{key}`) for bidirectional tracking.

When `DeleteByTag` is called, the cache:

1. Retrieves all L1 keys for the tag from the reverse index and deletes them.
2. Calls `l2.Adapter.DeleteByTag(ctx, tag)`, which handles L2 cleanup.
3. Publishes a `MessageTypeDelete` backplane message for each removed key.

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
