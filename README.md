# cache

A hybrid L1+L2 cache for Go, ported from [FusionCache](https://github.com/ZiggyCreatures/FusionCache). The core library has zero (one 😳) external dependencies.

---

## Features

- In-process L1 memory cache with optional distributed L2 backend
- Cache stampede protection via singleflight
- Fail-safe: return stale values when the factory or L2 fails
- Soft and hard factory timeouts with background completion
- Eager refresh: background factory call before expiry, zero-latency misses
- TTL jitter to spread expiry across nodes
- Tag-based bulk invalidation across L1 and L2
- Inter-node invalidation via a pluggable backplane
- Circuit breakers for L2 and the backplane
- Observable lifecycle events (`EventEmitter`)
- Type-safe generic helpers (`GetOrSet[T]`, `Get[T]`)
- Every pluggable point is a pure interface; bring your own adapters

---

## Requirements

Go 1.21 or later.

---

## Modules

| module                                                              | version |
| ------------------------------------------------------------------- | ------- |
| `github.com/coefficient-engineering/cache`                          | v0.2.0  |
| `github.com/coefficient-engineering/cache/adapters/l2/redis`        | v0.2.0  |
| `github.com/coefficient-engineering/cache/adapters/backplane/redis` | v0.2.0  |

---

## Installation

Core library (L1 only, no external dependencies):

```sh
go get github.com/coefficient-engineering/cache
```

Redis L2 adapter:

```sh
go get github.com/coefficient-engineering/cache/adapters/l2/redis
```

Redis backplane adapter:

```sh
go get github.com/coefficient-engineering/cache/adapters/backplane/redis
```

---

## Quick Start

This example uses the Redis L2 adapter and Redis backplane for a production-style setup. If you only need an in-process cache, skip the L2, serializer, and backplane setup and call `cache.New()` with only `WithDefaultEntryOptions`.

```go
package main

import (
	"context"
	"log/slog"
	"time"

	"github.com/coefficient-engineering/cache"
	redisbp "github.com/coefficient-engineering/cache/adapters/backplane/redis"
	redisl2 "github.com/coefficient-engineering/cache/adapters/l2/redis"
	"github.com/coefficient-engineering/cache/adapters/serializer/json"
	"github.com/redis/go-redis/v9"
)

func main() {
	ctx := context.Background()

	rdb := redis.NewClient(&redis.Options{Addr: "localhost:6379"})

	l2 := redisl2.New(rdb,
		redisl2.WithKeyPrefix("myapp:"),
		redisl2.WithLogger(slog.Default()),
	)

	bp := redisbp.New(rdb,
		redisbp.WithChannel("myapp:backplane"),
		redisbp.WithLogger(slog.Default()),
	)

	c, err := cache.New(
		cache.WithCacheName("myapp"),
		cache.WithL2(l2),
		cache.WithSerializer(&json.Serializer{}),
		cache.WithBackplane(bp),
		cache.WithLogger(slog.Default()),
		cache.WithL2CircuitBreaker(5, 10*time.Second),
		cache.WithDefaultEntryOptions(cache.EntryOptions{
			Duration:                 5 * time.Minute,
			IsFailSafeEnabled:        true,
			FailSafeMaxDuration:      2 * time.Hour,
			FailSafeThrottleDuration: 30 * time.Second,
			EagerRefreshThreshold:    0.9,
			FactorySoftTimeout:       100 * time.Millisecond,
			FactoryHardTimeout:       2 * time.Second,
			JitterMaxDuration:        10 * time.Second,
		}),
	)
	if err != nil {
		panic(err)
	}
	defer c.Close()

	type Product struct {
		ID   int
		Name string
	}

	product, err := cache.GetOrSet[*Product](ctx, c, "product:42",
		func(ctx context.Context, fctx *cache.FactoryExecutionContext) (*Product, error) {
			// Replace with a real database call.
			return &Product{ID: 42, Name: "Widget"}, nil
		},
	)
	if err != nil {
		panic(err)
	}

	_ = product
}
```

---

## Adapters

| adapter                                                              | purpose                                              |
| -------------------------------------------------------------------- | ---------------------------------------------------- |
| `github.com/coefficient-engineering/cache/adapters/l2/memory`        | In-process L2 using `sync.Map` (testing)             |
| `github.com/coefficient-engineering/cache/adapters/l2/redis`         | Redis L2 (accepts `redis.Cmdable`)                   |
| `github.com/coefficient-engineering/cache/adapters/backplane/memory` | In-process backplane via Go channel (testing)        |
| `github.com/coefficient-engineering/cache/adapters/backplane/noop`   | No-op backplane for single-node deployments          |
| `github.com/coefficient-engineering/cache/adapters/backplane/redis`  | Redis pub/sub backplane                              |
| `github.com/coefficient-engineering/cache/adapters/serializer/json`  | `encoding/json` serializer (stdlib, zero extra deps) |

To use a different backend, implement the relevant interface:

| interface               | package                                               | methods                                                    |
| ----------------------- | ----------------------------------------------------- | ---------------------------------------------------------- |
| `l1.Adapter`            | `github.com/coefficient-engineering/cache/l1`         | `Get`, `Set`, `Delete`, `CompareAndSwap`, `Range`, `Clear` |
| `l2.Adapter`            | `github.com/coefficient-engineering/cache/l2`         | `Get`, `Set`, `Delete`, `DeleteByTag`, `Clear`, `Ping`     |
| `backplane.Backplane`   | `github.com/coefficient-engineering/cache/backplane`  | `Publish`, `Subscribe`, `Close`                            |
| `serializer.Serializer` | `github.com/coefficient-engineering/cache/serializer` | `Marshal`, `Unmarshal`                                     |
