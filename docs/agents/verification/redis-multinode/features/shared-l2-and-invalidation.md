# Shared L2 and backplane invalidation

## User outcome

Node A writes `sku:1`; node B reads it from Redis. When A overwrites or deletes it, B stops serving its stale L1 copy within seconds. Tag invalidation also removes matching entries known only to B, without evicting unrelated entries or another cache namespace.

## How to reach it

Launch and Doctor from the surface README; `REDIS_ADDR` exported.

## How to drive it

```sh
mise exec -- go run ./scripts/verify multinode | tee tmp/verification/multinode.jsonl
```

For remote-only tag invalidation ([issue #38](https://github.com/coefficient-engineering/cache/issues/38)):

```sh
mise exec -- go run ./scripts/verify remote-tags | tee tmp/verification/remote-tags.jsonl
echo "exit ${PIPESTATUS[0]}"
```

## Proof

- `l2-key-written-to-redis`: `observed.payload_bytes > 0` for logical key `sku:1`, read through the public Redis adapter (not L1 or a guessed raw storage key).
- `node-b-reads-node-a-write-via-l2`: `value == "v1"`, `l2_hits == 1`.
- `node-b-sees-update-via-backplane`: `value == "v2"`, `backplane_received >= 1`.
- `delete-propagates-to-node-b-and-redis`: `node_b_found == false`, `redis_found == false` (the adapter returns no opaque payload).
- Exit status 0.

The `remote-tags` scenario runs both `l1-only` and `redis-l2` modes. Each runs `single` (`DeleteByTag("red")`) and `batch` (`DeleteByTags([]string{"red", "blue", "red"})`) operations with default asynchronous publishing:

- `*-sender-has-no-l1-seed`: A's L1-only read misses for every seeded key. Only B and the isolated receivers are seeded; setup uses `SkipBackplaneNotifications`.
- `*-fresh-node-b-l1-hit-before`: B returns its seeded value with L2 reads explicitly disabled before A invalidates.
- `*-node-b-type5-received`: B emits `EventBackplaneReceived` with `message_type == 5` and an empty `key`, after local processing. No sleep or cache-read polling is used as the delivery barrier.
- `*-after`: B's matching L1 entries miss; red/blue overlap and duplicate tags are covered. Single-tag invalidation preserves blue-only entries; both operations preserve green-tagged and untagged entries.
- `*-cache-name-isolated` and `*-key-prefix-isolated`: L1-only receivers sharing the pubsub channel retain every seeded entry. Each is checked after a matched public-cache publisher sends a harmless tag fence and that receiver emits its own type-5 receipt, so preservation is not a race against delivery.
- `redis-l2-*-l2-seeded` / `redis-l2-*-l2-after`: public Redis adapter reads show initial payloads, then misses for invalidated tags and retained unrelated/untagged payloads. B's L1 checks always bypass L2, so a refill cannot hide failed eviction.
- `*-fresh-write-after-delivery-readable`: B can write the invalidated red key again and immediately read it from L1.
- `run-prefixed-redis-data-and-control-keys-cleaned`: `remaining_keys == 0`; on the disposable empty instance, `redis-cli dbsize` must also return `0`.
- Every JSON check has `ok == true`, and exit status is 0. These are expected results; save the actual run's JSONL as evidence.

## Gotchas

The original `multinode` scenario polls propagation for up to 3s. `remote-tags` instead waits up to 5s per actual type-5 receipt, under a 30s scenario context. All caches and the Redis client are closed; cleanup uses its own finite context and deletes every `verify:remote-tags:<unixnano>:` data/control key, even after failure. Both scenarios keep nodes in one process with separate cache state and real Redis connections; a true multi-host proof needs separate processes.

The Redis adapter's versioned storage wrapper is separate from the core L2 envelope. Use adapter `Get`/`Set` for payload observations; direct `prefix+key` access no longer reaches stored data. Run-prefix enumeration is only for cleanup.

Successful asynchronous publication is not an all-node acknowledgement; this proof waits for the named receivers explicitly. It does not prove outage replay, mixed-version compatibility, or ordering against in-flight factories. Delayed/duplicate invalidations may evict a later matching write. Redis pubsub messages missed during disconnect are not replayed, and reconnect alone does not clear L1; use TTL, explicit `Clear`, or restart to reconcile missed inbound messages.
