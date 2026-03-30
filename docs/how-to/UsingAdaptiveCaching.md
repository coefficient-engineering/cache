# Using Adaptive Caching

Adaptive caching lets the factory function modify cache entry options at runtime based on the value it produces. Instead of using static configuration for every entry under a key pattern, each entry can have its own duration, tags, priority, or skip flags determined by the data itself.

## How it works

Every factory function receives a `*FactoryExecutionContext` as its second argument. This context contains a mutable `Options` pointer to the `EntryOptions` for the current operation. Changes the factory makes to `Options` are honoured when the value is stored.

```go
value, err := cache.GetOrSet(ctx, c, "product:42",
    func(ctx context.Context, fctx *cache.FactoryExecutionContext) (*Product, error) {
        p, err := db.GetProduct(ctx, 42)
        if err != nil {
            return nil, err
        }
        // Cache promotional products for less time.
        if p.IsPromotional {
            fctx.Options.Duration = 30 * time.Second
        }
        return p, nil
    },
    cache.WithDuration(10*time.Minute), // default for non-promotional
)
```

## Adapt duration based on data

Set `fctx.Options.Duration` inside the factory to control the TTL of the entry being stored:

```go
value, err := cache.GetOrSet(ctx, c, "config:"+key,
    func(ctx context.Context, fctx *cache.FactoryExecutionContext) (*Config, error) {
        cfg, err := configService.Get(ctx, key)
        if err != nil {
            return nil, err
        }
        if cfg.IsVolatile {
            fctx.Options.Duration = 15 * time.Second
        } else {
            fctx.Options.Duration = 1 * time.Hour
        }
        return cfg, nil
    },
)
```

## Add tags dynamically

Add tags based on the fetched value so bulk invalidation reflects actual data properties:

```go
value, err := cache.GetOrSet(ctx, c, "product:42",
    func(ctx context.Context, fctx *cache.FactoryExecutionContext) (*Product, error) {
        p, err := db.GetProduct(ctx, 42)
        if err != nil {
            return nil, err
        }
        fctx.Options.Tags = p.Categories // e.g. ["electronics", "sale"]
        return p, nil
    },
)

// Later: invalidate all products in the "sale" category.
c.DeleteByTag(ctx, "sale")
```

## Skip caching for certain values

Set skip flags to prevent storing specific values in L1 or L2:

```go
value, err := cache.GetOrSet(ctx, c, "user:"+id,
    func(ctx context.Context, fctx *cache.FactoryExecutionContext) (*User, error) {
        u, err := db.GetUser(ctx, id)
        if err != nil {
            return nil, err
        }
        if u.IsTemporary {
            fctx.Options.SkipL1Write = true
            fctx.Options.SkipL2Write = true
        }
        return u, nil
    },
)
```

The value is still returned to the caller, but it is not cached. The next request will call the factory again.

## Access stale values

When a stale entry exists (logically expired but within `FailSafeMaxDuration`), the factory receives it via `fctx.StaleValue` and `fctx.HasStaleValue`:

```go
value, err := cache.GetOrSet(ctx, c, "rates:usd",
    func(ctx context.Context, fctx *cache.FactoryExecutionContext) (*Rates, error) {
        rates, err := ratesAPI.Get(ctx)
        if err != nil {
            return nil, err
        }
        // If the value hasn't changed, extend the TTL.
        if fctx.HasStaleValue {
            old := fctx.StaleValue.(*Rates)
            if old.Version == rates.Version {
                fctx.Options.Duration = 30 * time.Minute
            }
        }
        return rates, nil
    },
    cache.WithDuration(5*time.Minute),
    cache.WithFailSafe(1*time.Hour, 30*time.Second),
)
```

## What happens on factory error

When the factory returns an error, the adapted options are discarded. `storeSafely` is never called, so no entry is written with the modified settings. If fail-safe is enabled and a stale value exists, the stale value is returned using the original (non-adapted) options.

## Interaction with other features

- **Fail-safe**: Adapted options only apply when the factory succeeds. A fail-safe activation uses the original entry options for throttle re-insertion.
- **Eager refresh**: Background refresh goroutines create their own `FactoryExecutionContext`. The factory can adapt options during eager refresh the same way it does during a normal `GetOrSet` call.
- **Soft/hard timeouts**: If the factory completes in the background after a soft timeout, the adapted options are used when storing the result (provided `AllowTimedOutFactoryBackgroundCompletion` is true).
- **Stampede protection**: Adapted options from the winning singleflight call are used for storage. All waiters receive the same value.

## See also

- [Adaptive Caching](../explanation/AdaptiveCaching.md) for the design rationale
- [Entry Options Reference](../reference/EntryOptions.md) for all fields that can be adapted
