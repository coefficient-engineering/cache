# GetOrSet Execution Flow

This document traces the complete decision tree executed on every `GetOrSet` call. Understanding this flow is the key to understanding how all features interact.

## Overview

```
GetOrSet(ctx, key, factory, opts...)
│
├─ Apply opts on top of DefaultEntryOptions
├─ Prefix the key
│
├─ [SkipL1Read?] ──NO──► Read L1
│   ├─ HIT (fresh) → maybeStartEagerRefresh → return value
│   ├─ HIT (stale) → staleEntry = entry
│   └─ MISS → continue
│
├─ Emit EventCacheMiss
│
└─ Enter singleflight (stampede protection)
    │
    ├─ Re-check L1 (may have been populated while waiting)
    │   └─ HIT (fresh) → return value (no factory needed)
    │
    ├─ [L2 configured AND !SkipL2Read AND !CB open?] ──YES──► Read L2
    │   ├─ HIT (fresh) → promote to L1 → return value
    │   ├─ HIT (stale) → staleEntry = l2Entry
    │   └─ MISS/error → continue
    │
    ├─ Emit EventFactoryCall
    │
    ├─ Call factory (with fail-safe + timeouts)
    │   ├─ SUCCESS → storeSafely → return value
    │   ├─ SOFT TIMEOUT + stale → return stale; factory continues in background
    │   ├─ HARD TIMEOUT + stale → return stale
    │   ├─ HARD TIMEOUT + no stale → return ErrFactoryHardTimeout
    │   ├─ ERROR + fail-safe + stale → re-promote stale → return stale
    │   └─ ERROR + no fail-safe → return error
    │
    └─ (end singleflight)
```

## Step-by-step walkthrough

### 1. Option resolution

```go
eo := applyOptions(c.opts.DefaultEntryOptions, opts)
```

A copy of the cache-wide `DefaultEntryOptions` is made. The per-call `EntryOption` functions are applied on top. The `Tags` slice is deep-copied to avoid shared backing arrays. The original defaults are never mutated.

### 2. Key prefixing

```go
pk := c.prefixedKey(key)
```

The configured `KeyPrefix` is prepended. All internal operations (L1, L2, tag index, singleflight) use the prefixed key.

### 3. L1 read

If `SkipL1Read` is false, the cache checks L1:

- **Fresh hit:** The entry's logical expiry is in the future. `EventCacheHit` is emitted. If `EagerRefreshThreshold` is set and enough time has elapsed, a background refresh starts. The value is returned (possibly auto-cloned).
- **Stale hit:** The entry is logically expired but physically present. It is saved as `staleEntry` for fail-safe use. If `AllowStaleOnReadOnly` were set (only relevant for `Get`, not `GetOrSet`), the stale value would be returned here.
- **Miss:** No entry in L1. Continue.

### 4. EventCacheMiss

Emitted after the L1 check fails to return a fresh value.

### 5. Stampede protection

The cache enters `singleflight.Do` (or `DoWithTimeout` if `LockTimeout` is set). All concurrent callers for the same prefixed key block here until the in-flight call completes.

### 6. L1 re-check

Inside singleflight, L1 is checked again. Another goroutine may have populated it while we were waiting for the lock. If a fresh entry is found, it is returned without calling the factory.

### 7. L2 read

If L2 is configured, `SkipL2Read` is false, and the L2 circuit breaker is not open:

- The L2 adapter is called with a context that may have `DistributedCacheHardTimeout` applied.
- The circuit breaker records the result.
- **Fresh hit:** The L2 entry is promoted to L1 (unless `SkipL1Write`). Tags are added to the tag index. The value is returned.
- **Stale hit:** The L2 entry is logically expired. If `staleEntry` is still nil, this becomes the fail-safe candidate. `EventL2Miss` is emitted.
- **Miss or error:** Continue to factory.

### 8. Factory call with fail-safe and timeouts

`executeWithFailSafe` calls `runFactoryWithTimeouts`, which manages the two-tier timeout model:

**Fast path (no timeouts):** The factory is called directly. On success, the value is returned. On error, fail-safe is evaluated.

**With timeouts:**

1. The factory runs in a goroutine.
2. Soft and hard timers are started.
3. The select loop waits for the first event:
   - **Factory completes:** If soft timeout already fired and `AllowTimedOutFactoryBackgroundCompletion` is true, the result is stored via `storeSafely`. Otherwise, the result is returned directly.
   - **Soft timeout fires:** If fail-safe is enabled and a stale value exists, the stale value is returned. A background goroutine continues waiting for the factory.
   - **Hard timeout fires:** The factory context is cancelled. If a stale value exists, it is returned. Otherwise, `ErrFactoryHardTimeout` is returned.
   - **Context cancelled:** The factory context is cancelled. The context error is returned.

**Fail-safe activation (after factory error):**

1. If `IsFailSafeEnabled` is false or `staleEntry` is nil, the error propagates.
2. Otherwise, `EventFailSafeActivated` is emitted.
3. The stale value is re-inserted into L1 with `FailSafeThrottleDuration` as its new logical expiry.
4. The stale value is returned (no error).

### 9. storeSafely

On a successful factory result:

```go
c.storeSafely(ctx, key, newValue, eo)
```

This:

1. Computes logical expiry (`now + Duration`) and physical expiry (`now + FailSafeMaxDuration` or `now + Duration` if fail-safe is off).
2. Creates a `cacheEntry`.
3. Writes to L1 (unless `SkipL1Write`). Adds tags to the tag index.
4. Writes to L2 (unless `SkipL2Write` or L2 is nil). Serializes the value, wraps in an `l2Envelope`, and calls `L2Adapter.Set`. This can be synchronous or background depending on `AllowBackgroundDistributedCacheOperations`.
5. Publishes a `MessageTypeSet` to the backplane (unless `SkipBackplaneNotifications` or backplane is nil). Can be synchronous or background.

### 10. Auto-clone

Before returning the final value, `maybeAutoClone` runs:

- If `EnableAutoClone` is false, the serializer is nil, or the value is nil, the value is returned as-is.
- Otherwise, the value is serialized and deserialized to produce a deep copy. Reflection is used to allocate a value of the correct concrete type.

### 11. Return

The value (and any error) is returned to the caller.

## Events emitted during a typical miss path

1. `EventCacheMiss` (L1 miss)
2. `EventL2Miss` (L2 miss, if L2 is configured)
3. `EventFactoryCall` (about to call factory)
4. `EventFactorySuccess` (factory returned)

## Events emitted during a fail-safe activation

1. `EventCacheMiss` (L1 miss)
2. `EventFactoryCall` (about to call factory)
3. `EventFailSafeActivated` (factory failed, stale value returned)
4. `EventFactoryError` (factory error logged)

## See also

- [Cache Stampede Protection](CacheStampedeProtection.md) for the singleflight mechanism
- [The Fail-Safe Mechanism](FailSafeMechanism.md) for stale value handling
- [Soft and Hard Timeouts](SoftAndHardTimeouts.md) for the timeout model
- [Eager Refresh](EagerRefresh.md) for background refresh
