# The Adapter Pattern

## Design principle

The core cache library has zero external dependencies beyond the Go standard library and `golang.org/x/sync`. Every pluggable integration point (L1 cache, L2 cache, backplane, serializer) is expressed as a Go interface. The library does not know or care what backend you use.

This means:

- A service using only in-process L1 caching pulls in zero cache-infrastructure dependencies.
- A service using Redis for L2 pulls in the Redis client library only because it explicitly imports the Redis adapter.
- A service using a proprietary caching system can implement the interface without modifying the core library.

## The four interfaces

### `l1.Adapter`

Controls the in-process memory cache. Methods: `Get`, `Set`, `Delete`, `LoadAndDelete`, `Range`, `CompareAndSwap`, `Clear`, `Close`.

The default implementation wraps `sync.Map`. You replace it when you need bounded caches (e.g., Ristretto, Theine, Otter) with eviction policies, size limits, and cost-based admission.

### `l2.Adapter`

Controls the distributed cache backend. Methods: `Get`, `Set`, `Delete`, `DeleteByTag`, `Clear`, `Ping`.

The cache passes already-serialized `[]byte` payloads. The adapter stores and retrieves opaque bytes. It does not need to understand Go types. Tag-to-key tracking is the adapter's responsibility.

### `backplane.Backplane`

Controls inter-node invalidation messaging. Methods: `Publish`, `Subscribe`, `Close`.

The transport is entirely up to the implementation: Redis pub/sub, NATS, Kafka, gRPC, or an in-process Go channel for testing.

### `serializer.Serializer`

Controls value encoding for L2 and auto-clone. Methods: `Marshal`, `Unmarshal`.

The built-in adapter uses `encoding/json`. You might replace it with MessagePack, Protocol Buffers, or any other codec.

## Why interfaces instead of concrete types

Using interfaces provides three properties:

1. **Dependency isolation.** The core library binary never grows because of a backend you don't use. Redis, Memcached, and NATS all live in separate Go modules with their own `go.mod`.

2. **Testability.** Tests inject in-memory adapters (or mocks) for fast, deterministic testing without requiring real infrastructure.

3. **Substitutability.** Switching from Redis to Memcached for L2 means changing one line of adapter construction. The cache configuration, entry options, and application code remain untouched.

## Dependency direction

The dependency always points inward:

```
Your adapter → Interface package → (nothing)
                   ↑
             Core library uses interface
```

The core library imports the interface packages (`l1`, `l2`, `backplane`, `serializer`). Adapters also import the interface packages. Neither the core nor the interface packages import any adapter.

This means adding a new adapter (e.g., a DynamoDB L2) requires:

1. Creating a new package that imports `github.com/coefficient-engineering/cache/l2`.
2. Implementing the `l2.Adapter` interface.
3. Importing the new package in your application.

No changes to the core library are needed.

## Compile-time interface verification

All built-in adapters include a compile-time check:

```go
var _ l1.Adapter = (*Adapter)(nil)
```

This verifies that the type satisfies the interface at compile time, catching missing methods before runtime. This pattern is recommended for custom adapters as well.

## See also

- [Architecture](Architecture.md) for the overall package structure
- [Writing L1 Adapters](../how-to/WritingL1Adapters.md)
- [Writing L2 Adapters](../how-to/WritingL2Adapters.md)
- [Writing Backplane Adapters](../how-to/WritingBackplaneAdapters.md)
- [Writing Serializer Adapters](../how-to/WritingSerializerAdapters.md)
