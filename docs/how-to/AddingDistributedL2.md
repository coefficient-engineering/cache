# Adding a Distributed L2 Cache

L2 is an optional distributed cache layer (e.g., Redis, Memcached) that sits behind the in-process L1 cache. When a key is missing from L1, the cache checks L2 before calling the factory. This reduces factory calls across multiple nodes sharing the same L2.

## Requirements

1. An L2 adapter implementing `l2.Adapter`
2. A serializer implementing `serializer.Serializer`

The cache uses the serializer to encode Go values into `[]byte` before writing to L2, and to decode them when reading back.

## Wire L2 with the in-memory adapter (testing)

```go
import (
    "github.com/coefficient-engineering/cache"
    l2memory "github.com/coefficient-engineering/cache/adapters/l2/memory"
    jsonserializer "github.com/coefficient-engineering/cache/adapters/serializer/json"
)

c, err := cache.New(
    cache.WithL2(l2memory.New()),
    cache.WithSerializer(&jsonserializer.Serializer{}),
)
```

## Wire L2 with the Redis adapter

Install the Redis L2 adapter module:

```bash
go get github.com/coefficient-engineering/cache/adapters/l2/redis
```

```go
import (
    "github.com/coefficient-engineering/cache"
    l2redis "github.com/coefficient-engineering/cache/adapters/l2/redis"
    jsonserializer "github.com/coefficient-engineering/cache/adapters/serializer/json"
    goredis "github.com/redis/go-redis/v9"
)

redisClient := goredis.NewClient(&goredis.Options{
    Addr: "localhost:6379",
})

c, err := cache.New(
    cache.WithL2(l2redis.New(redisClient, l2redis.WithKeyPrefix("myapp:"))),
    cache.WithSerializer(&jsonserializer.Serializer{}),
    cache.WithKeyPrefix("myapp:"),
)
```

The Redis adapter accepts `redis.Cmdable`, so it works with `*redis.Client`, `*redis.ClusterClient`, and `*redis.Ring`.

## The envelope format

The cache does not pass raw user values to the L2 adapter. It wraps them in an `l2Envelope` containing:

- `V`: the user value serialized by the configured `Serializer`
- `LE`: logical expiry as Unix milliseconds
- `PE`: physical expiry as Unix milliseconds
- `Tags`: associated tags

The envelope itself is always encoded with `encoding/json` by the core library, so all nodes can read it regardless of the user-facing serializer. The adapter stores and retrieves these bytes without needing to understand their contents.

## L2-specific options

Several `EntryOption` functions control L2 behavior:

| Option                                     | Effect                                                  |
| ------------------------------------------ | ------------------------------------------------------- |
| `WithDistributedCacheTimeouts(soft, hard)` | Bound L2 read/write latency                             |
| `WithBackgroundL2Ops()`                    | Make L2 writes fire-and-forget                          |
| `WithSkipL2()`                             | Skip L2 reads, writes, and backplane for this operation |
| `WithSkipL2ReadWhenStale()`                | Skip L2 read when L1 has a stale entry                  |

These can also be set in `EntryOptions` fields directly:

- `DistributedCacheDuration` overrides `Duration` for L2 TTL
- `DistributedCacheFailSafeMaxDuration` overrides `FailSafeMaxDuration` for L2
- `DistributedCacheSoftTimeout` / `DistributedCacheHardTimeout`
- `AllowBackgroundDistributedCacheOperations`
- `ReThrowDistributedCacheExceptions`

## Expire with L2

`Expire` also reaches L2 (unless `SkipL2Write`), so the next `GetOrSet` on any node calls the factory:

- **Fail-safe on** (in the `Expire` call's entry options): the stored envelope is rewritten with logical expiry = now. Its value, tags and physical expiry are kept (the TTL becomes the time left until physical expiry), so every node can still use the old value as a fail-safe fallback.
- **Fail-safe off:** the L2 entry is deleted, as `Delete` would.

Like `Delete`, the L2 call runs even while the L2 circuit breaker is open, records its outcome in the breaker, and honours `DistributedCacheHardTimeout`.

## Error handling

By default, L2 trouble never fails a call: an L2 adapter error is logged, reported as `EventL2Error`, and the cache carries on with L1 (a failed read is a miss, so `GetOrSet` calls the factory). Serialization errors are the exception: `ReThrowSerializationExceptions` defaults to `true`.

**L2 adapter errors: `ReThrowDistributedCacheExceptions`** (default `false`). When set, a failed L2 call that ran synchronously is returned, wrapped, so `errors.Is(err, adapterErr)` works:

| Method | What is returned |
| --- | --- |
| `Get` | A failed L2 read, as `(nil, false, err)`. |
| `GetOrSet` | A failed L2 read, without calling the factory and without fail-safe fallback. A failed L2 write of the factory's value is only logged; the value is returned. |
| `Set` | A failed L2 write, unless `AllowBackgroundDistributedCacheOperations` is set (background writes are only logged). |
| `Delete`, `DeleteByTag`, `Expire`, `Clear` | A failed L2 call. These always run synchronously and are not gated by the L2 circuit breaker. `Clear` uses the default entry options. |

- A `DistributedCacheHardTimeout` expiry counts as an L2 error.
- While the L2 circuit breaker is open, `Set`, `Get` and `GetOrSet` skip L2 and return no L2 error.
- The local L1 change is applied before the L2 call and is not rolled back. Mutations still publish to the backplane after an L2 failure; a rethrown backplane error is joined with the L2 error.

**Cache-wide default: `Options.SkipL2OnError`** (default `true`). Setting it to `false` makes `New()` turn on `ReThrowDistributedCacheExceptions` in the default entry options, so L2 errors propagate everywhere unless a call opts out:

```go
c, err := cache.New(
    cache.WithL2(adapter),
    cache.WithSerializer(&jsonserializer.Serializer{}),
    func(o *cache.Options) { o.SkipL2OnError = false },
)

// Opt a single call back out of rethrowing.
err = c.Set(ctx, "k", v, func(o *cache.EntryOptions) { o.ReThrowDistributedCacheExceptions = false })
```

**Serialization errors: `ReThrowSerializationExceptions`** (default `true`), independent of the two options above:

- Encoding a value for L2 fails: always logged at Error level. `Set` returns the error unless the L2 write runs in the background; the L1 write is kept. `GetOrSet` only logs it and returns the factory's value.
- Decoding an L2 entry fails: always logged. `Get` returns `(nil, false, err)`; `GetOrSet` returns the error without calling the factory. With the option off, the undecodable entry is treated as an L2 miss, so `GetOrSet` calls the factory and overwrites it.

## Circuit breaker

Protect L2 from being hammered during outages:

```go
cache.WithL2CircuitBreaker(5, 10*time.Second)
```

See [Using Circuit Breakers](UsingCircuitBreakers.md).

## Validation

`New()` returns an error if `WithL2()` is set without `WithSerializer()`:

```
cache: an L2 adapter requires a Serializer, add WithSerializer(...) to New()
```

## See also

- [Writing L2 Adapters](WritingL2Adapters.md) to implement your own
- [Architecture](../explanation/Architecture.md) for how L1 and L2 interact
