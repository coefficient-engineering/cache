# Using Jitter

Jitter adds a random amount of extra time to an entry's TTL, spreading out expirations to prevent many keys from expiring at the same instant. This is particularly useful in multi-node deployments where entries are cached around the same time (e.g., after a cold start or deployment).

## Configure jitter

### Per-call

```go
value, err := cache.GetOrSet(ctx, c, "key", factory,
    cache.WithJitter(2 * time.Second),
)
```

### As a default

```go
c, err := cache.New(
    cache.WithDefaultEntryOptions(cache.EntryOptions{
        Duration:          5 * time.Minute,
        JitterMaxDuration: 2 * time.Second,
    }),
)
```

## How it works

When an entry is stored, the effective TTL is:

```
effective TTL = base duration + random value in [0, JitterMaxDuration)
```

The random value is generated using `math/rand.Int63n`. Each entry gets a different random offset, so entries that were all cached at the same moment expire at slightly different times.

Jitter affects both L1 and L2 TTLs.

## Choosing a jitter value

The jitter should be large enough to spread out expirations but small enough that entries do not live significantly longer than intended. A common starting point is 1-5% of the entry's `Duration`.

| Duration  | Suggested JitterMaxDuration |
| --------- | --------------------------- |
| 1 minute  | 1-3 seconds                 |
| 5 minutes | 2-10 seconds                |
| 1 hour    | 30-60 seconds               |

## Disabling jitter

Set `JitterMaxDuration` to 0 (the default):

```go
cache.WithJitter(0) // disabled
```
