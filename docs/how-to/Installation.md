# Installation

## Core library

The core library has no external dependencies beyond `golang.org/x/sync`. Install it with:

```bash
go get github.com/coefficient-engineering/cache
```

This gives you the `Cache` interface, all option types, the default `sync.Map`-backed L1 adapter, and the generic helpers (`cache.GetOrSet[T]`, `cache.Get[T]`). No L2, backplane, or serializer adapters are included in the core module.

## Optional adapter modules

Adapter modules that depend on third-party libraries live in separate Go modules under `adapters/`. Each has its own `go.mod` and only pulls in its specific dependency.

### In-memory L2 adapter (testing)

```bash
go get github.com/coefficient-engineering/cache
```

The in-memory L2 adapter is part of the core module at `adapters/l2/memory`. No additional `go get` is needed.

Import path: `github.com/coefficient-engineering/cache/adapters/l2/memory`

### Redis L2 adapter

```bash
go get github.com/coefficient-engineering/cache/adapters/l2/redis
```

This pulls in `github.com/redis/go-redis/v9`.

Import path: `github.com/coefficient-engineering/cache/adapters/l2/redis`

### JSON serializer

```bash
go get github.com/coefficient-engineering/cache
```

The JSON serializer uses only `encoding/json` from the standard library and is part of the core module.

Import path: `github.com/coefficient-engineering/cache/adapters/serializer/json`

### In-memory backplane (testing)

```bash
go get github.com/coefficient-engineering/cache
```

Part of the core module.

Import path: `github.com/coefficient-engineering/cache/adapters/backplane/memory`

### No-op backplane

```bash
go get github.com/coefficient-engineering/cache
```

Part of the core module. Discards all messages silently.

Import path: `github.com/coefficient-engineering/cache/adapters/backplane/noop`

### Redis backplane

```bash
go get github.com/coefficient-engineering/cache/adapters/backplane/redis
```

This pulls in `github.com/redis/go-redis/v9` and `github.com/google/uuid`.

Import path: `github.com/coefficient-engineering/cache/adapters/backplane/redis`

## Module structure

The dependency graph is intentional. The core library never imports third-party cache or transport clients. Adapter modules reach inward by importing only the interface packages they implement:

```
cache (core)
  requires: golang.org/x/sync

adapters/l2/redis (separate module)
  requires: github.com/redis/go-redis/v9
  requires: github.com/coefficient-engineering/cache (for l2.Adapter interface)

adapters/backplane/redis (separate module)
  requires: github.com/redis/go-redis/v9
  requires: github.com/coefficient-engineering/cache (for backplane.Backplane interface)
```

A service that only needs L1 caching does not pull in Redis, Memcached, or any other infrastructure client.

## Minimum Go version

Go 1.21. This provides `log/slog` (structured logging in the standard library), `min()`/`max()` builtins, and a stable generics implementation.
