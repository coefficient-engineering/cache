# Backplane and Multi-Node Caching

## The consistency problem

In a multi-node deployment where each node has its own L1 in-process cache backed by a shared L2 (e.g., Redis), a write on one node does not automatically invalidate L1 entries on other nodes.

Example without a backplane:

1. Node A reads key "x" from L2 and caches it in its L1.
2. Node B writes a new value for key "x" to L2.
3. Node A still serves the old value from its L1 until the entry expires.

The L1 TTL determines how long stale data persists. For many applications, this staleness window is unacceptable.

## How the backplane solves this

The backplane is a pub/sub messaging layer between cache nodes. When the cache performs a mutation (Set, Delete, Expire, Clear), it publishes a message to the backplane. Other nodes receive the message and act on it:

| Message type        | Action on receiving node                                         |
| ------------------- | ---------------------------------------------------------------- |
| `MessageTypeSet`    | Evict the key from L1 (next read fetches from L2).               |
| `MessageTypeDelete` | Evict the key from L1 and remove tag associations.               |
| `MessageTypeExpire` | Set the L1 entry's logical expiry to now (retain for fail-safe). |
| `MessageTypeClear`  | Clear all L1 entries and the tag index.                          |

The key distinction: the backplane does not transfer values. It only transfers invalidation signals. The actual value is read from L2 on the next access.

## Self-message suppression

Every message carries a `SourceID` identifying the sender. The cache compares incoming `SourceID` against its own `NodeID` and ignores messages from itself. This prevents a node from redundantly evicting an entry it just wrote.

`NodeID` is set at construction via `WithNodeID`. If not specified, a random 16-byte hex string is generated.

## Message flow

```
Node A: Set("x", value)
  ├─ Write to L1
  ├─ Write to L2
  └─ Publish MessageTypeSet{Key: "x", SourceID: "node-a"} to backplane

Backplane delivers message to Node B and Node C

Node B: handleBackplaneMessage
  ├─ CacheName matches? Yes
  ├─ SourceID == NodeID? No (different node)
  └─ Evict "x" from L1

Node C: handleBackplaneMessage
  ├─ CacheName matches? Yes
  ├─ SourceID == NodeID? No (different node)
  └─ Evict "x" from L1
```

## CacheName filtering

The `Message.CacheName` field allows multiple cache instances to share one backplane channel. Each cache instance only processes messages with a matching `CacheName`. This is useful when a service has multiple cache instances (e.g., "products" and "users") but uses a single Redis pub/sub connection.

## Background vs. synchronous publishing

By default (`AllowBackgroundBackplaneOperations: true`), backplane publishes are fire-and-forget goroutines. The mutation returns to the caller without waiting for the publish to complete.

Setting `AllowBackgroundBackplaneOperations: false` makes publishes synchronous. Combined with `ReThrowBackplaneExceptions: true`, this causes backplane publish errors to be logged (though the current implementation does not propagate them to the caller from `publishBackplane`).

## Auto-recovery

When the backplane circuit breaker transitions from open to closed (recovery), the cache clears L1 (`BackplaneAutoRecovery: true` by default). This handles the scenario where the backplane was down and invalidation messages were lost:

1. Backplane goes down.
2. Other nodes write new values to L2 but cannot notify this node.
3. This node's L1 becomes stale.
4. Backplane recovers.
5. Circuit breaker closes, triggering auto-recovery.
6. L1 is cleared, forcing fresh reads from L2.

## `IgnoreIncomingBackplaneNotifications`

Setting this option to `true` causes the cache to discard all incoming backplane messages. The cache still publishes messages. This is useful for:

- Read-only replicas that should not evict their L1 based on other nodes' writes.
- Testing scenarios where you want to observe backplane behavior without side effects.

## Transport choices

The `backplane.Backplane` interface does not dictate the transport. The built-in adapters provide:

- `backplane/memory`: In-process Go channels with `Hub` for multi-node testing.
- `backplane/noop`: Discards all messages (explicit single-node).
- `backplane/redis`: Redis pub/sub for production.

Custom implementations can use NATS, Kafka, gRPC, or any other pub/sub system.

## See also

- [Adding a Backplane](../how-to/AddingBackplane.md) for setup steps
- [Writing Backplane Adapters](../how-to/WritingBackplaneAdapters.md) for implementing custom transports
- [Circuit Breakers](CircuitBreakers.md) for how the backplane circuit breaker works
