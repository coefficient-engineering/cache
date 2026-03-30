# Using Eager Refresh

Eager refresh starts a background factory call when a cache hit occurs late in the entry's lifetime, refreshing the value before it expires. Callers always receive the cached value immediately and never observe a cache miss due to expiration.

## Configure eager refresh

### Per-call

```go
value, err := cache.GetOrSet(ctx, c, "key", factory,
    cache.WithEagerRefresh(0.9),
)
```

### As a default

```go
c, err := cache.New(
    cache.WithDefaultEntryOptions(cache.EntryOptions{
        Duration:              5 * time.Minute,
        EagerRefreshThreshold: 0.9,
    }),
)
```

## How the threshold works

The threshold is a fraction between 0.0 and 1.0 (exclusive on both ends). It represents the proportion of `Duration` that must have elapsed before a background refresh is triggered.

With `Duration` = 5 minutes and `EagerRefreshThreshold` = 0.9:

- The entry is created at T=0.
- From T=0 to T=4m30s, cache hits return the value with no background work.
- From T=4m30s to T=5m (the last 10% of the entry's lifetime), a cache hit triggers a background refresh.
- At T=5m, the entry is logically expired.

If the background refresh succeeds before T=5m, the entry is replaced with a fresh value and the cycle restarts.

## Duplicate prevention

Only one background refresh runs per entry at a time. An atomic compare-and-swap guard prevents multiple concurrent goroutines from starting duplicate refreshes for the same key.

## Background context

The background refresh uses a new `context.Background()` with a timeout derived from `FactoryHardTimeout` (or 30 seconds if no hard timeout is configured). It does not inherit the original request's context, so a cancelled request does not cancel the refresh.

## Interaction with other features

- **Stampede protection**: The background refresh calls `storeSafely`, which writes to L1, L2, and the backplane.
- **Fail-safe**: If the background factory fails, the existing cached value is not replaced. The failure is logged. The entry continues serving from cache until it expires.
- **Timeouts**: The background factory respects `FactoryHardTimeout`. Soft timeouts do not apply to eager refresh because there is no caller waiting.

## Disabling eager refresh

Set the threshold to 0 or any value outside `(0.0, 1.0)`:

```go
cache.WithEagerRefresh(0) // disabled
```

## See also

- [Eager Refresh](../explanation/EagerRefresh.md) for the design rationale and threshold calculation details
