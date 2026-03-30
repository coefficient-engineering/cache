# Configuring Cache Options

Cache options are cache-wide settings applied once at construction time via `Option` functions passed to `cache.New()`. They cannot be changed after the cache is created.

## Basic usage

```go
c, err := cache.New(
    cache.WithCacheName("products"),
    cache.WithKeyPrefix("myapp:products:"),
    cache.WithLogger(slog.Default()),
)
```

Each `Option` function modifies the internal `Options` struct before the cache is constructed. You can pass as many as needed.

## Available options

### `WithCacheName(name string)`

Sets the name used in log messages, events, and backplane messages to distinguish this cache instance from others.

Default: `"default"`

```go
cache.WithCacheName("sessions")
```

### `WithKeyPrefix(prefix string)`

Prepends a string to every key before it is passed to L1, L2, and the backplane. Use this for namespace isolation when multiple caches share an L2 backend.

Default: `""` (no prefix)

```go
cache.WithKeyPrefix("myapp:products:")
```

### `WithL1(adapter l1.Adapter)`

Replaces the default in-process L1 cache. The default is an unbounded `sync.Map`-backed adapter. Use this to plug in a bounded cache like Ristretto, Theine, or Otter.

Default: `syncmap.New()` (unbounded `sync.Map`)

```go
import "github.com/coefficient-engineering/cache/adapters/l1/syncmap"

cache.WithL1(syncmap.New())
```

### `WithL2(adapter l2.Adapter)`

Sets the distributed (L2) cache adapter. If not set, the cache operates as a pure in-process memory cache. Requires `WithSerializer()` to also be set.

Default: `nil` (no L2)

```go
import l2memory "github.com/coefficient-engineering/cache/adapters/l2/memory"

cache.WithL2(l2memory.New())
```

### `WithSerializer(s serializer.Serializer)`

Sets the serializer used to encode and decode values for L2 storage. Required when `WithL2()` is set. Also required for `WithAutoClone()` to function.

Default: `nil`

```go
import jsonserializer "github.com/coefficient-engineering/cache/adapters/serializer/json"

cache.WithSerializer(&jsonserializer.Serializer{})
```

### `WithBackplane(bp backplane.Backplane)`

Enables inter-node cache invalidation. When a key is written, deleted, or expired, a message is published to the backplane so other nodes can evict their local L1 copies.

Default: `nil` (no backplane)

```go
import bpmemory "github.com/coefficient-engineering/cache/adapters/backplane/memory"

cache.WithBackplane(bpmemory.New("node-1"))
```

### `WithLogger(logger *slog.Logger)`

Sets the structured logger for internal diagnostics. If not set, all log output is discarded.

Default: `nil` (logs discarded)

```go
cache.WithLogger(slog.Default())
```

### `WithNodeID(id string)`

Sets the unique identifier for this cache node, used in backplane messages to suppress processing of self-sent notifications. If not set, a random 16-byte hex string is generated.

Default: random hex string

```go
cache.WithNodeID("node-us-east-1a")
```

### `WithDefaultEntryOptions(eo EntryOptions)`

Sets the baseline `EntryOptions` applied to every cache operation. Per-call `EntryOption` functions are applied on top of a copy of this value.

Default entry options when not overridden:

| Field                                      | Default           |
| ------------------------------------------ | ----------------- |
| `Duration`                                 | `5 * time.Minute` |
| `AllowTimedOutFactoryBackgroundCompletion` | `true`            |
| `AllowBackgroundBackplaneOperations`       | `true`            |
| `ReThrowSerializationExceptions`           | `true`            |
| `Priority`                                 | `PriorityNormal`  |

All other fields default to their zero values.

```go
cache.WithDefaultEntryOptions(cache.EntryOptions{
    Duration:            10 * time.Second,
    IsFailSafeEnabled:   true,
    FailSafeMaxDuration: 1 * time.Hour,
    FailSafeThrottleDuration: 30 * time.Second,
})
```

### `WithL2CircuitBreaker(threshold int, openDuration time.Duration)`

Configures the L2 circuit breaker. After `threshold` consecutive L2 errors, the circuit opens and all L2 calls are skipped for `openDuration`. A threshold of 0 disables the circuit breaker.

Default: disabled (threshold = 0)

```go
cache.WithL2CircuitBreaker(5, 10*time.Second)
```

### `WithBackplaneCircuitBreaker(threshold int, openDuration time.Duration)`

Configures the backplane circuit breaker with the same semantics as the L2 circuit breaker.

Default: disabled (threshold = 0)

```go
cache.WithBackplaneCircuitBreaker(3, 15*time.Second)
```

### `WithNodeID(id string)`

Sets the node identifier for backplane message filtering. See above.

## Validation

`New()` validates the configuration and returns an error if:

- `WithL2()` is set but `WithSerializer()` is not

## Immutability

The `Options` struct is copied during construction. Holding a reference to the original struct and modifying it after `New()` returns has no effect on the running cache.

## See also

- [Configuring Entry Options](ConfiguringEntryOptions.md) for per-call settings
