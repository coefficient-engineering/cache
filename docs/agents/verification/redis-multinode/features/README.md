# redis-multinode features

| Feature | User outcome |
| --- | --- |
| [shared-l2-and-invalidation](shared-l2-and-invalidation.md) | Writes share L2; key invalidation and `remote-tags` tag invalidation evict remote-only L1 entries while preserving unrelated entries and isolated cache namespaces. |
| [cluster-tags](cluster-tags.md) | Sharded Redis Cluster payloads support overlapping tag invalidation and Clear; metadata loss safely recovers, and application-owned maintenance reclaims TTL-zero bytes. |
| [backplane-rethrow](backplane-rethrow.md) | With synchronous rethrow enabled, a failed publish is returned from `Set`/`Delete`; defaults only log it. |
| [l2-rethrow](l2-rethrow.md) | With a failing L2, defaults keep serving from L1; `ReThrowDistributedCacheExceptions` / `SkipL2OnError: false` return the L2 error, and `ReThrowSerializationExceptions` returns encode/decode failures. |
| [expire-l2](expire-l2.md) | `Expire` makes every node call the factory next; with fail-safe the old L2 value stays a fallback, without it the Redis key is deleted. |
| [l2-soft-timeout](l2-soft-timeout.md) | With a fail-safe stale value, `GetOrSet` skips a slow L2 read after `DistributedCacheSoftTimeout` and calls the factory; without one it waits. |
