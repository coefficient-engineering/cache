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
