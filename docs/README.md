# Documentation

Cache is a hybrid L1+L2 cache library for Go with backplane support, stampede protection, fail-safe, and pluggable adapters. The core has zero external dependencies beyond the Go standard library and `golang.org/x/sync`.

## Tutorial

A hands-on walkthrough that builds a working cache step by step.

- [Getting Started](GettingStarted.md) - Create a cache, call `GetOrSet`, add fail-safe, add a distributed L2 layer

## How-to Guides

Task-oriented instructions for specific goals.

**Setup and Configuration**

- [Installation](how-to/Installation.md) - Install the core library and optional adapter modules
- [Configuring Cache Options](how-to/ConfiguringCacheOptions.md) - Set cache-wide settings with `Option` functions passed to `New()`
- [Configuring Entry Options](how-to/ConfiguringEntryOptions.md) - Override per-call settings with `EntryOption` functions

**Features**

- [Using Fail-Safe](how-to/UsingFailSafe.md) - Return stale values when the factory or L2 fails
- [Using Timeouts](how-to/UsingTimeouts.md) - Configure soft and hard timeouts for factories and L2 operations
- [Using Eager Refresh](how-to/UsingEagerRefresh.md) - Refresh entries in the background before they expire
- [Using Tags](how-to/UsingTags.md) - Tag entries and invalidate them in bulk with `DeleteByTag`
- [Using Jitter](how-to/UsingJitter.md) - Add random TTL variation to prevent thundering-herd expiry
- [Using Auto-Clone](how-to/UsingAutoClone.md) - Prevent callers from mutating cached values
- [Using Circuit Breakers](how-to/UsingCircuitBreakers.md) - Protect L2 and the backplane from being hammered during outages
- [Using Adaptive Caching](how-to/UsingAdaptiveCaching.md) - Let the factory modify entry options at runtime based on the fetched value
- [Observing Events](how-to/ObservingEvents.md) - Subscribe to cache lifecycle events for metrics and logging

**Infrastructure and Adapters**

- [Adding a Distributed L2 Cache](how-to/AddingDistributedL2.md) - Wire an L2 adapter and serializer for cross-node caching
- [Adding a Backplane](how-to/AddingBackplane.md) - Wire a backplane for inter-node cache invalidation
- [Writing L1 Adapters](how-to/WritingL1Adapters.md) - Implement the `l1.Adapter` interface for a custom in-process store
- [Writing L2 Adapters](how-to/WritingL2Adapters.md) - Implement the `l2.Adapter` interface for a custom distributed backend
- [Writing Backplane Adapters](how-to/WritingBackplaneAdapters.md) - Implement the `backplane.Backplane` interface for a custom transport
- [Writing Serializer Adapters](how-to/WritingSerializerAdapters.md) - Implement the `serializer.Serializer` interface for a custom encoding

## Reference

Reference documentation lives in the Go source code as godoc comments. Browse it with `go doc` or on [pkg.go.dev](https://pkg.go.dev/github.com/coefficient-engineering/cache):

- **Core package** — `go doc github.com/coefficient-engineering/cache` — `Cache` interface, `Options`, `EntryOptions`, events, errors
- **L1 adapter** — `go doc github.com/coefficient-engineering/cache/l1` — `l1.Adapter` interface
- **L2 adapter** — `go doc github.com/coefficient-engineering/cache/l2` — `l2.Adapter` interface
- **Backplane** — `go doc github.com/coefficient-engineering/cache/backplane` — `Backplane` interface, `Message`, `MessageType`
- **Serializer** — `go doc github.com/coefficient-engineering/cache/serializer` — `Serializer` interface
- **Built-in adapters** — `go doc github.com/coefficient-engineering/cache/adapters/...` — syncmap L1, memory L2, memory/noop backplane, JSON serializer

## Explanation

Background knowledge and design rationale.

- [Architecture](explanation/Architecture.md) - The L1+L2 hybrid model, the envelope format, and the zero-dependency core principle
- [Cache Stampede Protection](explanation/CacheStampedeProtection.md) - What a cache stampede is and how singleflight prevents it
- [Fail-Safe Mechanism](explanation/FailSafeMechanism.md) - Logical vs. physical expiry and why stale data is better than errors
- [The Adapter Pattern](explanation/TheAdapterPattern.md) - Why pluggable interfaces and zero core dependencies
- [Soft and Hard Timeouts](explanation/SoftAndHardTimeouts.md) - The two-tier timeout model and background completion
- [Eager Refresh](explanation/EagerRefresh.md) - How the threshold calculation works and the atomic guard against duplicates
- [Circuit Breakers](explanation/CircuitBreakers.md) - The three-state machine and how it protects the system during outages
- [Backplane and Multi-Node Caching](explanation/BackplaneAndMultiNode.md) - Why L1 caches diverge and how the backplane corrects this
- [GetOrSet Execution Flow](explanation/GetOrSetExecutionFlow.md) - The complete decision tree from L1 check to factory call to storage
- [Adaptive Caching](explanation/AdaptiveCaching.md) - How the factory execution context enables runtime option modification
