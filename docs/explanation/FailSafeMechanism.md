# The Fail-Safe Mechanism

## The problem

When a cache entry expires and the factory function fails (database error, network timeout, upstream outage), the caller receives an error. Under sustained outages, every request for that key hits the failing factory, amplifying the problem.

Fail-safe addresses this by returning the last known value even though it is stale. Having slightly outdated data is often preferable to returning errors.

## How it works

When `IsFailSafeEnabled` is true, the cache stores entries with two expiry timestamps:

- **Logical expiry** (`Duration`): When the value is considered stale from the caller's perspective.
- **Physical expiry** (`FailSafeMaxDuration`): When the value is physically removed from storage. This is always >= logical expiry.

After logical expiry but before physical expiry, the entry is available as a fail-safe candidate. If the factory fails, the cache returns this stale value instead of propagating the error.

## The activation sequence

1. A request arrives for a key whose L1 entry is logically expired (stale).
2. The stale entry is saved as `staleEntry`.
3. The cache enters stampede protection and attempts the factory.
4. The factory returns an error.
5. Because `IsFailSafeEnabled` is true and `staleEntry` is non-nil, the cache:
   - Logs a warning.
   - Emits `EventFailSafeActivated`.
   - Re-inserts the stale value into L1 with a short `FailSafeThrottleDuration` as its new logical expiry.
   - Returns the stale value (no error).

## Throttle duration

After fail-safe activates, the stale value is temporarily promoted back to "fresh" in L1 for `FailSafeThrottleDuration`. During this window, subsequent requests hit the re-promoted L1 entry and return immediately without calling the factory.

When the throttle duration expires, the next request attempts the factory again. If it still fails, fail-safe activates again with another throttle. This creates a retry cadence proportional to the throttle duration.

Without throttling, every single request during an outage would attempt the factory, receive an error, and then activate fail-safe. The throttle reduces factory call volume during outages.

## L2 fail-safe

Fail-safe works across L2 as well. The `l2Envelope` stored in L2 carries both the logical expiry (`LE`) and physical expiry (`PE`) as Unix milliseconds. Any node reading from L2 can determine whether an entry is a valid fail-safe candidate, even if it wasn't the node that originally wrote the entry.

This means a node that has never seen a key before can read a stale entry from L2 and use it as a fail-safe fallback.

## Configuration

Required fields for fail-safe:

| Field                      | Purpose                                                                            |
| -------------------------- | ---------------------------------------------------------------------------------- |
| `IsFailSafeEnabled`        | Activates the mechanism.                                                           |
| `FailSafeMaxDuration`      | How long the value persists physically in the backing store. Must be > `Duration`. |
| `FailSafeThrottleDuration` | How long the stale value is treated as fresh after fail-safe activates.            |

Optional overrides:

| Field                                 | Purpose                                                            |
| ------------------------------------- | ------------------------------------------------------------------ |
| `DistributedCacheFailSafeMaxDuration` | L2-specific physical TTL override.                                 |
| `AllowStaleOnReadOnly`                | Returns stale entries from `Get` without requiring a factory call. |

## Interaction with timeouts

Fail-safe and timeouts work together:

- **Soft timeout fires:** If a stale value exists, it is returned immediately. The factory continues in the background and stores its result when done.
- **Hard timeout fires:** If a stale value exists, it is returned. Otherwise, `ErrFactoryHardTimeout` is returned.
- **Factory error:** The stale value is returned and re-promoted with the throttle duration.

In all cases, fail-safe requires both `IsFailSafeEnabled == true` and a non-nil stale entry. If no stale value is available (first-ever request for a key, or physical expiry passed), the error propagates normally.

## See also

- [Using Fail-Safe](../how-to/UsingFailSafe.md) for configuration steps
- [Soft and Hard Timeouts](SoftAndHardTimeouts.md) for how timeouts interact with fail-safe
