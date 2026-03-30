# Eager Refresh

## The problem

Standard cache expiry creates a latency spike: the entry expires, the next request encounters a miss, and the caller waits for the factory to produce a fresh value. For latency-sensitive applications, this periodic spike is unacceptable.

## How eager refresh works

Eager refresh starts a background factory call before the entry expires, so callers never observe a miss. It works by monitoring cache hits:

1. A request hits a fresh L1 entry.
2. The cache computes what fraction of the entry's `Duration` has elapsed since creation.
3. If the elapsed fraction exceeds `EagerRefreshThreshold`, a background refresh goroutine is started.
4. The caller receives the cached value immediately (zero additional latency).
5. The background goroutine calls the factory and, on success, stores the result via `storeSafely` (updating L1, L2, and the backplane).

## Threshold calculation

The threshold is a `float32` in the range `(0.0, 1.0)`. It represents the fraction of `Duration` that must elapse before a refresh is triggered.

Example with `Duration = 10m` and `EagerRefreshThreshold = 0.9`:

- At 8 minutes elapsed (0.8): No refresh. Entry is returned normally.
- At 9.1 minutes elapsed (0.91): Refresh starts in background. Entry is returned immediately.
- At 10 minutes: Entry reaches logical expiry. If the refresh completed, L1 now has a fresh entry. If not, the next request triggers a normal factory call.

Values outside `(0.0, 1.0)` disable eager refresh. A value of 0 also disables it.

## Deduplication

Only one background refresh runs per entry at a time. The `cacheEntry` struct contains an `eagerRefreshRunning` atomic counter:

- Before starting a refresh, the cache performs a compare-and-swap from 0 to 1.
- If the CAS fails, another goroutine is already refreshing this entry.
- When the refresh completes (success or failure), the counter is reset to 0.

This prevents multiple concurrent requests from spawning duplicate background goroutines for the same key.

## Background goroutine lifecycle

The refresh goroutine:

1. Creates a new `context.Background()` context with a timeout of `FactoryHardTimeout` (or 30 seconds if no hard timeout is set). It does not use the original request context because that context may be cancelled before the refresh completes.
2. Calls the factory.
3. On success, calls `storeSafely` to update L1, L2, and the backplane.
4. On failure, logs a warning and returns. The entry is not modified.
5. Resets the `eagerRefreshRunning` counter.

## Interaction with other features

- **Stampede protection:** Eager refresh runs outside singleflight. It does not compete with or block normal cache operations.
- **Fail-safe:** If the eager refresh factory fails, fail-safe is not activated. The existing entry remains in L1 unchanged. Fail-safe only activates on direct `GetOrSet` misses.
- **Backplane:** The `storeSafely` call from a successful eager refresh publishes a backplane message, causing other nodes to evict their L1 copy and read the fresh value from L2.

## When to use

Eager refresh is most valuable for:

- High-traffic keys where a single cache miss causes a noticeable latency spike.
- Entries with predictable access patterns (regularly accessed within their TTL).
- Factory calls that are moderately expensive (database queries, API calls).

Eager refresh is less useful for:

- Infrequently accessed keys (they may expire without ever being read past the threshold).
- Very short TTLs (the overhead of background goroutines may not be worth it).

## See also

- [Using Eager Refresh](../how-to/UsingEagerRefresh.md) for configuration steps
