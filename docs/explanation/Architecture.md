# Architecture

This document describes the high-level architecture of the cache library, how the components are layered, and the dependency relationships between packages.

## Two-layer caching model

The cache operates with two layers:

- **L1 (in-process memory cache):** A fast, local cache that stores Go values directly. Every cache instance has an L1 layer. L1 is accessed without network calls and without serialization.
- **L2 (distributed cache):** An optional remote cache that stores serialized bytes. Multiple cache nodes can share a single L2 backend (e.g., Redis, Memcached). L2 is accessed through the `l2.Adapter` interface.

When both layers are active, L1 serves as a local read-through cache in front of L2. A cache hit in L1 avoids the network round-trip to L2 entirely.

## Backplane

When multiple nodes share an L2 backend, each node maintains its own L1. Without coordination, L1 entries can become stale when another node writes a new value to L2.

The backplane solves this. It is a pub/sub messaging layer that broadcasts invalidation messages between nodes. When node A writes key "x", it publishes a message. Nodes B and C receive the message and evict "x" from their L1, so the next read fetches the fresh value from L2.

## Package structure

The library is organized into three tiers:

### Core library (zero external dependencies beyond `golang.org/x/sync`)

The root package `github.com/coefficient-engineering/cache` contains all core logic:

- `cache.go`: The `Cache` interface, the unexported `cache` struct, generic helpers, and method implementations.
- `options.go`: `Options` (cache-wide), `EntryOptions` (per-operation), and functional option types.
- `builder.go`: `New()` constructor and `Option` functions.
- `entry.go`: Internal `cacheEntry` and `l2Envelope` types.
- `stampede.go`: Singleflight wrapper for stampede protection.
- `failsafe.go`: Fail-safe activation logic.
- `timeout.go`: Soft and hard timeout machinery.
- `eagerrefresh.go`: Background eager refresh.
- `events.go`: `EventEmitter` and all event types.
- `circuitbreaker.go`: Three-state circuit breaker.
- `tags.go`: In-memory tag-to-key reverse index.
- `errors.go`: Sentinel errors.

### Interface packages (no external dependencies)

Each pluggable extension point is expressed as a Go interface in its own package:

- `l1/adapter.go`: `l1.Adapter` for in-process caches.
- `l2/adapter.go`: `l2.Adapter` for distributed cache backends.
- `backplane/backplane.go`: `backplane.Backplane` for inter-node messaging.
- `serializer/serializer.go`: `serializer.Serializer` for value encoding.

These packages import nothing outside the standard library. Third-party adapter implementations import the interface package, not the other way around.

### Adapter packages (optional, each owns its own imports)

Built-in adapters live under `adapters/`:

- `adapters/l1/syncmap/`: Default L1, backed by `sync.Map`. No external dependencies.
- `adapters/l2/memory/`: In-process L2 for testing. No external dependencies.
- `adapters/l2/redis/`: Redis L2. Requires `github.com/redis/go-redis/v9`.
- `adapters/backplane/memory/`: In-process backplane for testing. No external dependencies.
- `adapters/backplane/noop/`: No-op backplane. No external dependencies.
- `adapters/backplane/redis/`: Redis pub/sub backplane. Requires `github.com/redis/go-redis/v9`.
- `adapters/serializer/json/`: `encoding/json` serializer. No external dependencies.

Redis adapters are in separate Go modules with their own `go.mod`, so importing the core library does not pull in Redis dependencies.

## Dependency direction

The core library never imports adapter packages. Adapters import the interface packages. This means:

- A service using only L1 caching has zero external dependencies beyond `golang.org/x/sync`.
- A service using Redis L2 only pulls in the Redis client library because it explicitly imports the Redis adapter.
- Custom adapters written outside this repository follow the same pattern: import the interface package, implement it.

## Generics strategy

The `Cache` interface stores and returns `any`. Generic package-level functions (`GetOrSet[T]`, `Get[T]`) provide compile-time type safety at the call site without complicating the core. This is the same approach used by `sync.Map` in the standard library: an untyped core with typed wrappers.

Go does not allow generic methods on interface types, so the generic helpers are package-level functions that accept a `Cache` as a parameter.

## Internal components

### Clock

`internal/clock/clock.go` defines a `Clock` interface with a single `Now()` method. The production implementation uses `time.Now()`. Tests inject a fake clock for deterministic timing.

### Singleflight

The library uses `golang.org/x/sync/singleflight` for stampede protection. This is the only external dependency of the core package.

## See also

- [The Adapter Pattern](TheAdapterPattern.md) for why the interfaces are designed this way
- [GetOrSet Execution Flow](GetOrSetExecutionFlow.md) for the full decision tree
