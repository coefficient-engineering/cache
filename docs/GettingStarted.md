# Getting Started

This tutorial walks through building a working cache from scratch. By the end, we will have an in-process L1 cache with fail-safe and a distributed L2 layer.

Each step produces a runnable program. We start simple and add features incrementally.

## Prerequisites

- Go 1.21 or later
- A new or existing Go module

Install the core library:

```bash
go get github.com/coefficient-engineering/cache
```

## Step 1: A basic L1 cache

Create a file called `main.go`:

```go
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/coefficient-engineering/cache"
)

func main() {
	c, err := cache.New(
		cache.WithDefaultEntryOptions(cache.EntryOptions{
			Duration: 10 * time.Second,
		}),
	)
	if err != nil {
		log.Fatal(err)
	}
	defer c.Close()

	ctx := context.Background()

	// GetOrSet calls the factory on a cache miss and stores the result.
	value, err := cache.GetOrSet(ctx, c, "greeting", func(ctx context.Context) (string, error) {
		fmt.Println("factory called")
		return "hello, world", nil
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("first call:", value)

	// The second call hits L1. The factory is not called again.
	value, err = cache.GetOrSet(ctx, c, "greeting", func(ctx context.Context) (string, error) {
		fmt.Println("factory called")
		return "hello, world", nil
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("second call:", value)
}
```

Run it:

```bash
go run main.go
```

Expected output:

```
factory called
first call: hello, world
second call: hello, world
```

The factory runs once. The second call returns the cached value from L1 without calling the factory.

`cache.GetOrSet` is a generic helper that provides type safety at the call site. The first type parameter (`string` in this case) determines the return type. The underlying `Cache` interface stores `any` values internally.

`New()` returns a `Cache` interface. With no adapter options, it creates a pure in-process memory cache backed by `sync.Map`. The default entry duration is 5 minutes if not overridden.

## Step 2: Add fail-safe

Fail-safe returns a stale cached value when the factory fails, instead of propagating the error to the caller. This requires configuring three things:

- `IsFailSafeEnabled` (set automatically by `WithFailSafe`)
- `FailSafeMaxDuration` controls how long the entry physically exists in the cache (the window during which it can serve as a fallback)
- `FailSafeThrottleDuration` controls how long the stale value is temporarily promoted back to "fresh" after a failure, preventing the factory from being called on every request during an outage

Replace `main.go` with:

```go
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/coefficient-engineering/cache"
)

func main() {
	c, err := cache.New(
		cache.WithDefaultEntryOptions(cache.EntryOptions{
			Duration:                 2 * time.Second,
			IsFailSafeEnabled:       true,
			FailSafeMaxDuration:     1 * time.Minute,
			FailSafeThrottleDuration: 5 * time.Second,
		}),
	)
	if err != nil {
		log.Fatal(err)
	}
	defer c.Close()

	ctx := context.Background()

	// First call: factory succeeds, value is cached.
	value, err := cache.GetOrSet(ctx, c, "product:1", func(ctx context.Context) (string, error) {
		return "Widget", nil
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("first call:", value)

	// Wait for the entry to become logically stale (past Duration).
	fmt.Println("waiting for entry to go stale...")
	time.Sleep(3 * time.Second)

	// Second call: factory fails, but fail-safe returns the stale value.
	value, err = cache.GetOrSet(ctx, c, "product:1", func(ctx context.Context) (string, error) {
		return "", errors.New("database is down")
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("second call (fail-safe):", value)
}
```

Run it:

```bash
go run main.go
```

Expected output:

```
first call: Widget
waiting for entry to go stale...
second call (fail-safe): Widget
```

The factory failed on the second call, but the caller received the stale value instead of an error. The stale value was promoted back to "fresh" for 5 seconds (`FailSafeThrottleDuration`), so subsequent requests within that window will not call the factory again.

For more detail on how fail-safe works, see [Fail-Safe Mechanism](explanation/FailSafeMechanism.md). For configuration options, see [Using Fail-Safe](how-to/UsingFailSafe.md).

## Step 3: Add a distributed L2 cache

L2 is an optional second cache layer, typically a shared store like Redis or Memcached. When a key is missing from L1, the cache checks L2 before calling the factory. This is useful in multi-node deployments where each node has its own L1 but shares a common L2.

For this tutorial, we use the in-memory L2 adapter so no external infrastructure is needed. In production, you would use a Redis or similar adapter instead (see [Adding a Distributed L2 Cache](how-to/AddingDistributedL2.md)).

An L2 adapter requires a serializer to encode values into bytes. We use the built-in JSON serializer.

Replace `main.go` with:

```go
package main

import (
	"context"
	"fmt"
	"log"
	"time"

	"github.com/coefficient-engineering/cache"
	l2memory "github.com/coefficient-engineering/cache/adapters/l2/memory"
	jsonserializer "github.com/coefficient-engineering/cache/adapters/serializer/json"
)

func main() {
	c, err := cache.New(
		cache.WithL2(l2memory.New()),
		cache.WithSerializer(&jsonserializer.Serializer{}),
		cache.WithDefaultEntryOptions(cache.EntryOptions{
			Duration: 10 * time.Second,
		}),
	)
	if err != nil {
		log.Fatal(err)
	}
	defer c.Close()

	ctx := context.Background()

	factoryCallCount := 0

	factory := func(ctx context.Context) (string, error) {
		factoryCallCount++
		fmt.Printf("factory called (call #%d)\n", factoryCallCount)
		return "cached-value", nil
	}

	// First call: cache miss on both L1 and L2. Factory is called.
	value, err := cache.GetOrSet(ctx, c, "key:1", factory)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("first call:", value)

	// Clear L1 to simulate a different node or a process restart.
	_ = c.Clear(ctx, false) // false = do not clear L2
	fmt.Println("L1 cleared")

	// Second call: L1 miss, but L2 hit. Factory is NOT called.
	value, err = cache.GetOrSet(ctx, c, "key:1", factory)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println("second call:", value)
	fmt.Printf("factory was called %d time(s)\n", factoryCallCount)
}
```

Run it:

```bash
go run main.go
```

Expected output:

```
factory called (call #1)
first call: cached-value
L1 cleared
second call: cached-value
factory was called 1 time(s)
```

The factory was called only once. After clearing L1, the second call found the value in L2 and promoted it back to L1 without calling the factory.

When L2 is configured, values written through `GetOrSet` or `Set` are stored in both L1 and L2. An L2 adapter requires a `Serializer` because L2 stores opaque `[]byte` payloads. If you pass `WithL2()` without `WithSerializer()`, `New()` returns an error.

## Next steps

- [Configuring Entry Options](how-to/ConfiguringEntryOptions.md) for all per-call settings
- [Using Timeouts](how-to/UsingTimeouts.md) to bound factory and L2 latency
- [Using Eager Refresh](how-to/UsingEagerRefresh.md) to refresh entries before they expire
- [Adding a Backplane](how-to/AddingBackplane.md) for inter-node invalidation
- [Architecture](explanation/Architecture.md) for a high-level view of how the layers interact
