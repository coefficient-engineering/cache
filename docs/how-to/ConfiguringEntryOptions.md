# Configuring Entry Options

Entry options control per-operation behavior. They are applied on top of the cache-wide defaults on every call to `GetOrSet`, `Get`, `Set`, `Delete`, `DeleteByTag`, and `Expire`.

## How defaults and overrides work

When you call a cache method, the library copies the `DefaultEntryOptions` from the cache's `Options` and then applies each `EntryOption` function you pass. The stored defaults are never mutated.

```go
// These two calls use different durations, but the cache's defaults are unchanged.
cache.GetOrSet(ctx, c, "key1", factory, cache.WithDuration(10*time.Second))
cache.GetOrSet(ctx, c, "key2", factory, cache.WithDuration(30*time.Second))
```

If you do not pass any `EntryOption` functions, the defaults from `WithDefaultEntryOptions()` are used as-is.

## Available EntryOption functions

### Expiry

**`WithDuration(d time.Duration)`** sets how long the entry is considered fresh. After this duration, the entry is logically expired. With fail-safe enabled, the entry remains physically stored for `FailSafeMaxDuration` and can be used as a fallback.

```go
cache.WithDuration(30 * time.Second)
```

**`WithJitter(max time.Duration)`** adds a random additional TTL in `[0, max)` to both L1 and L2 entries. This spreads out expiration times across entries to prevent many keys from expiring at the same instant.

```go
cache.WithJitter(2 * time.Second)
```

### Fail-safe

**`WithFailSafe(maxDuration, throttleDuration time.Duration)`** enables fail-safe for this operation. Sets `IsFailSafeEnabled` to `true`, `FailSafeMaxDuration` to `maxDuration`, and `FailSafeThrottleDuration` to `throttleDuration`.

```go
cache.WithFailSafe(1*time.Hour, 30*time.Second)
```

**`WithAllowStaleOnReadOnly()`** allows `Get` (read-only, no factory) to return stale entries. Without this, `Get` returns `(nil, false, nil)` when the entry is logically expired.

```go
cache.WithAllowStaleOnReadOnly()
```

### Factory timeouts

**`WithFactoryTimeouts(soft, hard time.Duration)`** sets the soft and hard timeouts for the factory function. The soft timeout returns a stale value (if fail-safe is enabled and a stale value exists) while the factory continues in the background. The hard timeout cancels the factory and returns an error or stale value.

Pass 0 for either to disable that timeout.

```go
cache.WithFactoryTimeouts(100*time.Millisecond, 2*time.Second)
```

### L2 timeouts

**`WithDistributedCacheTimeouts(soft, hard time.Duration)`** sets timeouts for L2 read and write operations. Works the same way as factory timeouts.

```go
cache.WithDistributedCacheTimeouts(500*time.Millisecond, 2*time.Second)
```

### Eager refresh

**`WithEagerRefresh(threshold float32)`** starts a background factory call when a cache hit occurs after the given fraction of `Duration` has elapsed. The value must be in `(0.0, 1.0)`. A threshold of `0.9` means refresh starts at 90% of the entry's lifetime.

```go
cache.WithEagerRefresh(0.9)
```

### Tags

**`WithTags(tags ...string)`** associates string labels with the entry for bulk invalidation via `DeleteByTag`. Tags are additive: calling `WithTags` multiple times appends to the list.

```go
cache.WithTags("category:electronics", "featured")
```

### Priority and size

**`WithPriority(p EvictionPriority)`** hints to the L1 eviction policy. Higher priority entries are evicted last. Constants: `PriorityLow` (-1), `PriorityNormal` (0), `PriorityHigh` (1), `PriorityNeverRemove` (2).

```go
cache.WithPriority(cache.PriorityHigh)
```

**`WithSize(n int64)`** sets an arbitrary weight for L1 eviction decisions. Bounded L1 adapters use this; the default `sync.Map` adapter ignores it.

```go
cache.WithSize(1024)
```

### Skip flags

**`WithSkipL2()`** bypasses all L2 reads, L2 writes, and backplane notifications for this operation.

```go
cache.WithSkipL2()
```

**`WithSkipL2ReadWhenStale()`** skips checking L2 for a newer version when L1 has a stale entry. Useful when L2 is a local (not shared) store.

```go
cache.WithSkipL2ReadWhenStale()
```

### L2 background operations

**`WithBackgroundL2Ops()`** makes L2 writes fire-and-forget. This reduces latency but means write failures are logged rather than returned.

```go
cache.WithBackgroundL2Ops()
```

### Auto-clone

**`WithAutoClone()`** deep-clones values returned from L1 via serialize/deserialize before returning them to the caller. This prevents callers from mutating cached objects. Requires a serializer to be configured on the cache.

```go
cache.WithAutoClone()
```

### Stampede protection

**`WithLockTimeout(d time.Duration)`** sets the maximum time to wait for the stampede protection lock. If the timeout elapses, the caller proceeds without the lock. A value of 0 means wait indefinitely.

```go
cache.WithLockTimeout(5 * time.Second)
```

## Combining options

Pass multiple `EntryOption` functions in a single call:

```go
value, err := cache.GetOrSet(ctx, c, "key", factory,
    cache.WithDuration(30*time.Second),
    cache.WithFailSafe(1*time.Hour, 30*time.Second),
    cache.WithFactoryTimeouts(100*time.Millisecond, 2*time.Second),
    cache.WithTags("products"),
)
```

## See also

- [Configuring Cache Options](ConfiguringCacheOptions.md) for cache-wide settings
