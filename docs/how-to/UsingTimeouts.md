# Using Timeouts

Soft and hard timeouts bound how long the cache waits for a factory function or an L2 operation before taking an alternative action. This prevents slow backends from blocking callers indefinitely.

## Factory timeouts

### Configure per-call

```go
value, err := cache.GetOrSet(ctx, c, "key", factory,
    cache.WithFactoryTimeouts(100*time.Millisecond, 2*time.Second),
)
```

### Configure as a default

```go
c, err := cache.New(
    cache.WithDefaultEntryOptions(cache.EntryOptions{
        FactorySoftTimeout: 100 * time.Millisecond,
        FactoryHardTimeout: 2 * time.Second,
    }),
)
```

### Soft timeout

When the soft timeout fires:

- If fail-safe is enabled and a stale value exists, the stale value is returned to the caller immediately.
- The factory continues running in the background. If it succeeds and `AllowTimedOutFactoryBackgroundCompletion` is `true` (the default), the result is stored in the cache for future callers.
- If no stale value is available, the cache continues waiting until the hard timeout or factory completion.

### Hard timeout

When the hard timeout fires:

- If fail-safe is enabled and a stale value exists, the stale value is returned.
- Otherwise, `ErrFactoryHardTimeout` is returned.
- The factory's context is cancelled.

### No timeouts

When both `FactorySoftTimeout` and `FactoryHardTimeout` are 0 (the default), the factory runs with no time limit and the cache waits for it to complete.

## L2 timeouts

L2 reads and writes can also be bounded:

```go
value, err := cache.GetOrSet(ctx, c, "key", factory,
    cache.WithDistributedCacheTimeouts(500*time.Millisecond, 2*time.Second),
)
```

The `DistributedCacheHardTimeout` is applied as a `context.WithTimeout` on the context passed to the L2 adapter's `Get` and `Set` methods.

## Background completion

`AllowTimedOutFactoryBackgroundCompletion` controls whether a factory that was timed out (soft or hard) is allowed to store its result when it eventually completes. This is `true` by default.

When `true`:

- After a soft timeout, a background goroutine waits for the factory result and stores it.
- After a hard timeout, the factory's context is cancelled so it typically stops on its own.

When `false`:

- After a soft timeout, the factory's context is cancelled immediately.

```go
cache.WithDefaultEntryOptions(cache.EntryOptions{
    AllowTimedOutFactoryBackgroundCompletion: false,
})
```

## Example: fast responses with background refresh

This configuration returns a stale value after 100ms but lets the factory finish in the background:

```go
c, err := cache.New(
    cache.WithDefaultEntryOptions(cache.EntryOptions{
        Duration:                                 5 * time.Minute,
        IsFailSafeEnabled:                       true,
        FailSafeMaxDuration:                     1 * time.Hour,
        FailSafeThrottleDuration:                30 * time.Second,
        FactorySoftTimeout:                       100 * time.Millisecond,
        FactoryHardTimeout:                       5 * time.Second,
        AllowTimedOutFactoryBackgroundCompletion: true,
    }),
)
```

A slow factory call plays out as:

1. At 100ms, the soft timeout fires. The caller gets the stale value.
2. The factory continues in the background.
3. If the factory succeeds before 5s, the result is stored and future callers get the fresh value.
4. If the factory exceeds 5s, its context is cancelled.

## See also

- [Soft and Hard Timeouts](../explanation/SoftAndHardTimeouts.md) for the design rationale
- [Using Fail-Safe](UsingFailSafe.md) for how stale values are used as fallbacks
