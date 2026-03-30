# Soft and Hard Timeouts

## The problem

Factory functions and L2 operations access external systems (databases, APIs, remote caches). These calls can be slow or hang. Without timeouts, a slow factory blocks all goroutines waiting on that key via singleflight, potentially causing cascading latency throughout the service.

## Two-tier timeout model

The cache implements a two-tier timeout model for both factory calls and L2 operations:

```
Factory call ──────────────────────────────────────────────────►

◄── soft timeout ──►
                    │  Return stale now; factory continues in background.
                    │
◄──────────── hard timeout ───────────────────────────────────►
                                   │  Cancel factory. Return error or stale.
```

### Soft timeout

The soft timeout is the preferred wait time. When it fires:

- If fail-safe is enabled and a stale value exists, the stale value is returned to the caller immediately. The factory continues running in a background goroutine. When it eventually completes, its result is stored in the cache (if `AllowTimedOutFactoryBackgroundCompletion` is true).
- If no stale value is available, the soft timeout has no effect. The cache continues waiting for the factory or the hard timeout.

The soft timeout provides low-latency responses during temporary slowness while still capturing the factory result for future requests.

### Hard timeout

The hard timeout is the absolute maximum wait. When it fires:

- The factory context is cancelled.
- If fail-safe is enabled and a stale value exists, the stale value is returned.
- If no stale value is available, `ErrFactoryHardTimeout` is returned.

The hard timeout guarantees bounded latency regardless of factory behavior.

## Factory timeout configuration

| Field                                      | Type            | Description                                                               |
| ------------------------------------------ | --------------- | ------------------------------------------------------------------------- |
| `FactorySoftTimeout`                       | `time.Duration` | Max wait before returning stale. 0 disables.                              |
| `FactoryHardTimeout`                       | `time.Duration` | Absolute max wait. 0 means wait indefinitely.                             |
| `AllowTimedOutFactoryBackgroundCompletion` | `bool`          | When true (default), a timed-out factory stores its result on completion. |

## L2 timeout configuration

L2 operations use a simpler model: only a hard timeout is applied via `context.WithTimeout`.

| Field                         | Type            | Description                                                     |
| ----------------------------- | --------------- | --------------------------------------------------------------- |
| `DistributedCacheSoftTimeout` | `time.Duration` | Reserved for L2 soft timeout. 0 disables.                       |
| `DistributedCacheHardTimeout` | `time.Duration` | Applied to both L2 reads and writes. 0 means wait indefinitely. |

## Background completion

When `AllowTimedOutFactoryBackgroundCompletion` is true and the soft timeout fires:

1. The stale value is returned to the caller.
2. A background goroutine waits for the factory result.
3. If the factory succeeds, `storeSafely` writes the fresh value to L1, L2, and the backplane.
4. A safety timeout (the hard timeout duration, or 60 seconds if no hard timeout is set) ensures the background goroutine does not leak.

When `AllowTimedOutFactoryBackgroundCompletion` is false, the factory context is cancelled as soon as the soft timeout fires. No background goroutine is created.

## Interaction with fail-safe

Timeouts and fail-safe are complementary:

- Without fail-safe: soft timeouts have no effect (there is no stale value to return). Hard timeouts return `ErrFactoryHardTimeout`.
- With fail-safe: soft timeouts return stale values for low latency. Hard timeouts return stale values as a last resort. The factory error is absorbed.

For timeouts to return stale values, there must actually be a stale value available. On the first-ever request for a key (cold cache), there is no stale entry, and the timeout returns an error.

## See also

- [Using Timeouts](../how-to/UsingTimeouts.md) for configuration steps
- [The Fail-Safe Mechanism](FailSafeMechanism.md) for how stale values are managed
