# Adding a Backplane

A backplane enables inter-node cache invalidation. When a key is written, deleted, or expired on one node, a message is published to the backplane so other nodes can evict their local L1 copies. Without a backplane, L1 caches on different nodes can diverge after mutations.

## Wire a backplane

### In-memory backplane (testing)

The in-memory backplane uses Go channels. Use the `Hub` to connect multiple cache instances in the same process for testing multi-node scenarios.

```go
import (
    "github.com/coefficient-engineering/cache"
    bpmemory "github.com/coefficient-engineering/cache/adapters/backplane/memory"
)

hub := bpmemory.NewHub()

c1, _ := cache.New(
    cache.WithBackplane(bpmemory.NewWithHub("node-1", hub)),
    cache.WithNodeID("node-1"),
)

c2, _ := cache.New(
    cache.WithBackplane(bpmemory.NewWithHub("node-2", hub)),
    cache.WithNodeID("node-2"),
)
```

### Redis backplane

Install the Redis backplane adapter module:

```bash
go get github.com/coefficient-engineering/cache/adapters/backplane/redis
```

```go
import (
    "github.com/coefficient-engineering/cache"
    bpredis "github.com/coefficient-engineering/cache/adapters/backplane/redis"
    goredis "github.com/redis/go-redis/v9"
)

redisClient := goredis.NewClient(&goredis.Options{
    Addr: "localhost:6379",
})

c, err := cache.New(
    cache.WithBackplane(bpredis.New(redisClient,
        bpredis.WithChannel("myapp:cache:backplane"),
        bpredis.WithNodeID("node-us-east-1a"),
    )),
    cache.WithNodeID("node-us-east-1a"),
)
```

The Redis backplane requires `*redis.Client` (not `redis.Cmdable`) because pub/sub needs a dedicated connection.

### No-op backplane

For explicit single-node deployments where you want the code to be clear that no inter-node communication occurs:

```go
import bpnoop "github.com/coefficient-engineering/cache/adapters/backplane/noop"

c, err := cache.New(
    cache.WithBackplane(bpnoop.New()),
)
```

## Message types

The backplane transmits five message types:

| Type                          | Trigger                                  | Receiver action                                    |
| ----------------------------- | ---------------------------------------- | -------------------------------------------------- |
| `MessageTypeSet` (1)          | A key was written via `Set` or `GetOrSet` | Evict key from L1 so next access fetches from L2     |
| `MessageTypeDelete` (2)       | A key was deleted via `Delete`           | Evict key from L1                                   |
| `MessageTypeExpire` (3)       | A key was logically expired via `Expire` | Mark key as logically expired in L1                 |
| `MessageTypeClear` (4)        | `Clear` was called                       | Clear all L1 entries and the tag index              |
| `MessageTypeDeleteByTags` (5) | `DeleteByTag` or `DeleteByTags`           | Hard-evict L1 entries carrying any supplied tag      |

Tag messages carry `Tags` and an empty `Key`. Receivers use their own tag indexes, so eviction includes remote-only and stale/fail-safe entries even when the sender has no matching local keys. One tag-level message is attempted per nonempty invalidation call, including after an L2 error, unless notifications are skipped; `DeleteByTags` with an empty slice is a no-op.

Every message carries `CacheName` and `KeyPrefix`; receivers require an exact match for both, including for Clear. This isolates backplane notifications only: use separately isolated L2 adapter namespaces to isolate L2 tags and Clear.

### Delivery and upgrades

Delivery is best-effort and asynchronous. A successful synchronous publish does not acknowledge remote processing. Duplicate/delayed messages can evict fresh entries, and concurrent or in-flight factory writes are not globally fenced. Redis pub/sub does not replay missed messages. See [Backplane and Multi-Node Caching](../explanation/BackplaneAndMultiNode.md#delivery-and-concurrent-writes) for the full contract.

Upgrade participating nodes and custom transports together to preserve the new `Tags` and `KeyPrefix` fields. Old nodes ignore type 5; upgraded recipients with nonempty prefixes reject legacy messages missing `KeyPrefix`. There is no per-key fallback or mixed-version coherence guarantee.

## Self-message filtering

Each message carries a `SourceID` identifying the sender node. The cache skips processing messages where `SourceID` matches its own `NodeID`. Make sure the `NodeID` passed to the cache matches the node ID used by the backplane adapter.

## Auto-recovery

`BackplaneAutoRecovery` (default: `true`) clears only the **local** L1 and tag index when the outbound publish circuit breaker recovers from open/half-open to closed after a successful publish. It does not clear other nodes or replay lost notifications.

A Redis subscriber reconnect alone does not trigger this recovery: inbound subscription outages need not open the outbound publish circuit breaker. Without that transition, reconcile through TTL expiry, an explicit `Clear`, or restart.

There is no `With...` helper; to disable recovery, pass a custom `Option`:

```go
c, err := cache.New(
    cache.WithBackplane(bp),
    func(o *cache.Options) { o.BackplaneAutoRecovery = false },
)
```

`AutoRecoveryMaxRetries` and `AutoRecoveryDelay` are deprecated and have no effect.

## Skipping backplane notifications

For a specific operation:

```go
// WithSkipL2() also skips backplane notifications
cache.WithSkipL2()
```

Or set `SkipBackplaneNotifications` directly in `EntryOptions`.

To ignore all incoming backplane messages (e.g., for a read-only replica):

```go
cache.New(
    cache.WithDefaultEntryOptions(cache.EntryOptions{...}),
    // set IgnoreIncomingBackplaneNotifications on Options directly
)
```

Note: `IgnoreIncomingBackplaneNotifications` is a field on `Options`, not an `Option` function. Set it by constructing `Options` manually if needed.

## See also

- [Backplane and Multi-Node Caching](../explanation/BackplaneAndMultiNode.md) for why L1 caches diverge and how the backplane corrects this
- [Writing Backplane Adapters](WritingBackplaneAdapters.md) to implement your own
- [Using Circuit Breakers](UsingCircuitBreakers.md) for backplane circuit breaker configuration
