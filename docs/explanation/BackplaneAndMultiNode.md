# Backplane and Multi-Node Caching

## The consistency problem

In a multi-node deployment where each node has its own L1 in-process cache backed by a shared L2 (e.g., Redis), a write on one node does not automatically invalidate L1 entries on other nodes.

Example without a backplane:

1. Node A reads key "x" from L2 and caches it in its L1.
2. Node B writes a new value for key "x" to L2.
3. Node A still serves the old value from its L1 until the entry expires.

The L1 TTL determines how long stale data persists. For many applications, this staleness window is unacceptable.

## How the backplane solves this

The backplane is a pub/sub messaging layer between cache nodes. When the cache performs a mutation (`Set`, `Delete`, `Expire`, `Clear`, `DeleteByTag`, or `DeleteByTags`), it publishes a message to the backplane. Other nodes act on delivered messages:

| Message type              | Action on receiving node                                         |
| ------------------------- | ---------------------------------------------------------------- |
| `MessageTypeSet`          | Evict the key from L1 (next read fetches from L2).                 |
| `MessageTypeDelete`       | Evict the key from L1 and remove tag associations.                 |
| `MessageTypeExpire`       | Set the L1 entry's logical expiry to now (retain for fail-safe).   |
| `MessageTypeClear`        | Clear all L1 entries and the tag index.                            |
| `MessageTypeDeleteByTags` | Hard-evict L1 entries carrying any supplied tag using the local index. |

The key distinction: the backplane does not transfer values. It only transfers invalidation signals. The actual value is read from L2 on the next access.

Tag invalidation sends one message containing `Tags` with an empty `Key`, even when the sender has no matching local entries or its L2 invalidation fails. `DeleteByTags` deduplicates its tags and sends one message for the batch; an empty batch is a no-op. Each receiver finds matching entries in **its own** tag index, including remote-only copies. Tag invalidation removes stale/fail-safe copies too, unlike `Expire`; evicted copies are no longer available as fail-safe fallbacks.

## Self-message suppression

Every message carries a `SourceID` identifying the sender. The cache compares incoming `SourceID` against its own `NodeID` and ignores messages from itself. This prevents a node from redundantly evicting an entry it just wrote.

`NodeID` is set at construction via `WithNodeID`. If not specified, a random 16-byte hex string is generated.

## Message flow

```
Node A: Set("x", value)
  ├─ Write to L1
  ├─ Write to L2
  └─ Publish Message{Type: MessageTypeSet, CacheName: "products",
                    KeyPrefix: "app:", Key: "x", Tags: nil,
                    SourceID: "node-a"} to backplane

Backplane delivers message to Node B and Node C

Node B: handleBackplaneMessage
  ├─ CacheName matches? Yes
  ├─ KeyPrefix matches exactly? Yes
  ├─ SourceID == NodeID? No (different node)
  └─ Evict "x" from L1

Node C: handleBackplaneMessage
  ├─ CacheName matches? Yes
  ├─ KeyPrefix matches exactly? Yes
  ├─ SourceID == NodeID? No (different node)
  └─ Evict "x" from L1
```

## Namespace filtering and upgrades

Every message must exactly match **both** the receiver's `CacheName` and `KeyPrefix`, including tag and Clear messages. Empty values are not wildcards. This allows independent cache namespaces to share a backplane channel.

Backplane filtering does not isolate L2 storage, tags, or Clear operations. The core prefixes entry keys, but an L2 adapter's tag/control namespace is separate. Independent caches must also use separately isolated L2 adapter namespaces (for Redis, configure the adapter's own `WithKeyPrefix`). A different cache name or core key prefix alone does not isolate shared L2 tag invalidations.

Coordinate upgrades across all participating cache nodes and custom transports. The `Backplane` interface is unchanged, but messages now include `Tags` and `KeyPrefix` and type 5 (`MessageTypeDeleteByTags`). Old nodes ignore type 5; upgraded recipients with nonempty prefixes reject legacy messages lacking `KeyPrefix`. Transports must preserve the complete message. There is no per-key fallback or mixed-version coherence guarantee.

## Background vs. synchronous publishing

By default (`AllowBackgroundBackplaneOperations: true`), backplane publishes are fire-and-forget goroutines. The mutation returns to the caller without waiting for the publish to complete.

Setting `AllowBackgroundBackplaneOperations: false` makes publishes synchronous. Combined with `ReThrowBackplaneExceptions: true`, a failed publish is returned as an error from `Set`, `Delete`, `Expire`, `DeleteByTag`, `DeleteByTags` and `Clear` (wrapping the transport error, so `errors.Is` works). Local changes are not rolled back. `GetOrSet` still returns its factory value and only logs the publish failure.

L2 invalidation retains its existing error policy: an L2 failure is logged and only returned when configured to rethrow. Backplane publishing is still attempted after an L2 error; rethrown L2 and backplane errors are joined. Skipping notifications prevents remote eviction. Failed or skipped L2 invalidation can leave data that refills an evicted L1 entry.

## Delivery and concurrent writes

Delivery is **best-effort and asynchronous**, including when the publish itself is synchronous. A successful `Publish` is not an acknowledgement that any or all receivers have processed the message. The cache provides no reliable replay or cross-node acknowledgement protocol.

- A delivered tag message evicts entries matching the receiver's tag index when it is processed; duplicate or delayed messages can therefore evict fresh matching entries.
- Concurrent writes and in-flight factory results may survive invalidation or be evicted. There is no global generation fence or ordering by factory start time. Any stronger L2-adapter ordering guarantee is separate from this L1 contract.
- `EventBackplaneReceived` is emitted **after** local message processing. Observing it can be a deterministic receipt barrier for that receiver, not an all-node acknowledgement or a fence against later writes. Tag events have an empty `Key`.
- Redis pub/sub messages missed during a disconnect are not replayed. Reconnecting the subscriber alone does not clear L1.

## Auto-recovery

`BackplaneAutoRecovery` (default: `true`) clears only the **local** L1 and tag index when this node's outbound publish circuit breaker transitions from open/half-open to closed after a successful publish. It does not clear remote nodes or replay missed messages.

An inbound subscriber disconnect need not cause outbound publish failures or open that breaker. Reconnect alone therefore does not imply auto-recovery. Without the outbound recovery transition, use TTL expiry, an explicit `Clear`, or restart to reconcile L1. A cleared L1 still depends on successful L2 invalidation or fresh factory data to avoid refilling old data.

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
