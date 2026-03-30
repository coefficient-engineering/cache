# Adaptive Caching

## The problem

Static cache configuration forces every entry under a given key pattern to use the same TTL, the same tags, and the same storage flags. In practice, the correct caching behaviour often depends on the value itself:

- A promotional product should expire faster than a regular product.
- A volatile configuration flag needs a short TTL; a stable one can be cached for hours.
- A temporary user record should skip L2 entirely.
- When an upstream value hasn't changed since the last fetch, extending its TTL avoids unnecessary factory calls.

Without adaptive caching, you either use a lowest-common-denominator TTL for all entries, or you write separate cache keys and factory logic for different data shapes.

## How it works

Every `FactoryFunc` receives a `*FactoryExecutionContext` alongside the standard `context.Context`:

```go
type FactoryFunc func(ctx context.Context, fctx *FactoryExecutionContext) (any, error)
```

The `FactoryExecutionContext` contains:

- **`Options *EntryOptions`** — A mutable pointer to the `EntryOptions` for this cache operation. The factory can modify any field: `Duration`, `Tags`, `SkipL1Write`, `Priority`, etc.
- **`StaleValue any`** — The previously cached (now stale) value, if one exists.
- **`HasStaleValue bool`** — Distinguishes a nil stale value from "no stale entry".

After the factory returns successfully, the cache reads the (potentially modified) `EntryOptions` and uses them in `storeSafely` to write the entry to L1, L2, and the backplane. If the factory returns an error, the adapted options are discarded.

## The pointer mechanism

The `Options` field in `FactoryExecutionContext` is a pointer to the local `EntryOptions` copy created at the start of each `GetOrSet` call. This means:

1. `GetOrSet` copies the default entry options and applies any per-call `EntryOption` funcs.
2. `fctx.Options` is set to point at this local copy.
3. The factory runs and may mutate fields through the pointer.
4. When the factory returns, `storeSafely` reads from the same local copy — seeing the factory's mutations.

No allocation or copying overhead beyond what already existed. The factory simply writes through a pointer that `storeSafely` already reads from.

## Stale value access

When fail-safe is enabled and a logically expired entry exists in L1 or L2, the cache populates `fctx.StaleValue` and sets `fctx.HasStaleValue` to true before calling the factory. The factory can use the stale value to make intelligent decisions:

- Compare versions to decide whether the new value is actually different.
- Extend the TTL if the data is unchanged.
- Compute a delta instead of a full replacement.

On a first-ever cache miss (no stale entry exists), `HasStaleValue` is false and `StaleValue` is nil.

## Error handling

Adaptive options are only used when the factory succeeds. The sequence is:

1. Factory runs and modifies `fctx.Options`.
2. Factory returns `(value, nil)` — options are used by `storeSafely`.
3. Factory returns `(nil, err)` — `storeSafely` is never called. If fail-safe activates, the stale value is re-inserted using the original (pre-adaptation) throttle duration.

This means a factory cannot accidentally corrupt cache behaviour by modifying options and then returning an error.

## Eager refresh

Background eager-refresh goroutines create their own `FactoryExecutionContext` with a copy of the entry options and the current cached value as the stale value. The factory can adapt options during eager refresh exactly as it does during a normal `GetOrSet` call.

## See also

- [Using Adaptive Caching](../how-to/UsingAdaptiveCaching.md) for configuration steps and examples
- [Entry Options Reference](../reference/EntryOptions.md) for all fields that can be adapted
- [GetOrSet Execution Flow](GetOrSetExecutionFlow.md) for where adaptive caching fits in the full call path
