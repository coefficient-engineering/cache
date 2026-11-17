# v0.5.0

Requires Go 1.26 or later, unchanged from v0.4.0. The core, Redis L2 adapter, and Redis backplane adapter use the same release version.

## Changes

- Add `Cache.DeleteByTags` for union invalidation with deduplicated tags and bounded failure reports. `l2.TagsDeleter` is an optional batching capability; existing `l2.Adapter` implementations do not need another method.
- Make Redis tag invalidation and Clear safe across Cluster hash slots without concentrating payloads on one shard. Generation snapshots are validated against authoritative primary metadata.
- Recover missing Redis control metadata under a fresh incarnation without resurrecting old records, including ambiguous initialization outcomes and slot migration. Recovery clients do not share mutable notification-handler registries with caller clients.
- Add application-owned Redis maintenance to reclaim invalidated unread records, including TTL-zero records. Cleanup preserves concurrent replacements and follows physical Ring shards after remapping.
- Propagate tag invalidation to remote-only L1 entries using receiver-owned tag indexes. Cache name and key prefix isolate notifications. Coupled L1/index mutations are synchronized so concurrent retagging preserves unrelated replacements; ordinary reads remain concurrent.
- Update dependencies to `golang.org/x/sync` v0.25.0, go-redis v9.23.0, and miniredis v2.40.0 ([#36](https://github.com/coefficient-engineering/cache/pull/36)); update the pinned `mise-action` to v5.2.5 ([#33](https://github.com/coefficient-engineering/cache/pull/33)).

Resolves [#37](https://github.com/coefficient-engineering/cache/issues/37) and [#38](https://github.com/coefficient-engineering/cache/issues/38), and includes bulk invalidation from [#35](https://github.com/coefficient-engineering/cache/pull/35).

## Breaking changes and migration

### Core API

Custom implementations and generated mocks of `Cache` must implement `DeleteByTags(context.Context, []string, ...EntryOption) error`. The base `l2.Adapter` and `backplane.Backplane` interfaces are unchanged.

### Redis L2

- `redisl2.New` returns `(*Adapter, error)`. Handle construction failures before passing the adapter to `cache.New`.
- Native standalone and Cluster clients are supported. Custom `redis.Cmdable` wrappers require explicit `WithPrimaryAccess`. Rings additionally require a coherent standalone/Cluster `WithControlClient`.
- Storage moves to the versioned `fc:v2:` namespace. This is a **cold cutover**: old and new adapters do not share data or invalidations. Coordinate deployment and retire legacy keys separately; do not rely on mixed-version coherence.
- Start `RunMaintenance(ctx)` in an application-owned goroutine. Cancel and join it before closing the injected Redis clients. Valid TTL-zero entries remain indefinite; invalidated bytes are eventually reclaimed only while successful maintenance coverage continues.
- Tag invalidation and Clear are logical operations, not immediate physical payload deletion. One control hash per namespace is an intentional metadata throughput hotspot.

See [Redis lifecycle and client capabilities](docs/how-to/AddingDistributedL2.md#redis-invalidation-and-lifecycle).

### Backplane protocol

- New `MessageTypeDeleteByTags` (5) carries `Tags`; every message carries `KeyPrefix` as well as `CacheName`.
- Upgrade all nodes and custom transports together. Transports must preserve the full message. Old nodes ignore type 5; upgraded nodes with nonempty prefixes reject legacy missing-prefix messages. There is no per-key tag fallback or mixed-version guarantee.
- `EventBackplaneReceived` now fires **after** accepted-message local processing, providing a local completion barrier, not an acknowledgement from other nodes.

See [backplane schema and coordinated upgrades](docs/how-to/WritingBackplaneAdapters.md#the-message-type).

## Consistency boundaries

Delivery remains best-effort and asynchronous. Delayed or duplicate tag messages can evict newer matching entries; concurrent writes and in-flight factory results are not globally fenced. Delivered tag invalidation hard-evicts fail-safe copies too. Redis pub/sub does not replay notifications lost during disconnection; reconnect alone does not clear L1. Reconcile missed inbound messages through TTL, explicit Clear, or restart. Existing outbound circuit-breaker recovery only clears the recovering node's L1.

L2 failures and skip flags retain their existing policies and may permit refilling an evicted L1 from uninvalidated L2 data. Independently isolate L2 adapter namespaces when isolating cache namespaces. Redis rollback or restore of acknowledged history is outside the no-resurrection guarantee.

## Publishing

After release checks pass, publish these tags at the same final release-preparation commit:

```sh
git tag -a v0.5.0 -m 'v0.5.0'
git tag -a adapters/l2/redis/v0.5.0 -m 'Redis L2 v0.5.0'
git tag -a adapters/backplane/redis/v0.5.0 -m 'Redis backplane v0.5.0'
git push --atomic origin refs/tags/v0.5.0 refs/tags/adapters/l2/redis/v0.5.0 refs/tags/adapters/backplane/redis/v0.5.0
```

Publishing is separate from preparation. Do not republish the accidentally issued and retracted v1.0.x tags. Publish the GitHub draft only after all three v0.5.0 module tags are available.
