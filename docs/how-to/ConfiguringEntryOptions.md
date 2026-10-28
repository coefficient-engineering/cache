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

**`WithDistributedCacheTimeouts(soft, hard time.Duration)`** sets L2 timeouts. The hard timeout bounds every L2 call. The soft timeout only bounds `GetOrSet`'s L2 read when a fail-safe stale value exists; past it, L2 is skipped and the factory runs with that fallback.

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

### Priority (deprecated) and size

`EntryOptions.Priority`, `EvictionPriority`, its constants, and `WithPriority` are retained for source compatibility. Priority is not passed to L1 adapters and has no effect.

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

**`WithBackgroundL2Ops()`** makes the L2 write of `Set` and `GetOrSet` fire-and-forget. This reduces latency but means write failures are logged rather than returned. L2 reads and the L2 calls of `Delete`, `DeleteByTag` and `Clear` always run synchronously.

```go
cache.WithBackgroundL2Ops()
```

### L2 and serialization errors

There is no `With...` helper for these; set the fields with an inline `EntryOption`.

- **`ReThrowDistributedCacheExceptions`** (default `false`, or `true` when `Options.SkipL2OnError` is `false`): a failed synchronous L2 call is returned, wrapped, instead of only being logged and reported as `EventL2Error`.
- **`ReThrowSerializationExceptions`** (default `true`): a failure to encode a value for L2, or to decode an L2 entry, is returned. When off, it is logged and a bad L2 entry counts as a miss.

```go
func(o *cache.EntryOptions) { o.ReThrowDistributedCacheExceptions = true }
```

Which methods return which errors is detailed in [Adding a Distributed L2: Error handling](AddingDistributedL2.md#error-handling).

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
