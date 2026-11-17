# Shared L2 and backplane invalidation

## User outcome

Node A writes `sku:1`; node B reads it from Redis. When A overwrites or deletes it, B stops serving its stale L1 copy within seconds.

## How to reach it

Launch and Doctor from the surface README; `REDIS_ADDR` exported.

## How to drive it

```sh
mise exec -- go run ./scripts/verify multinode | tee tmp/verification/multinode.jsonl
```

## Proof

- `l2-key-written-to-redis`: `observed.payload_bytes > 0` for logical key `sku:1`, read through the public Redis adapter (not L1 or a guessed raw storage key).
- `node-b-reads-node-a-write-via-l2`: `value == "v1"`, `l2_hits == 1`.
- `node-b-sees-update-via-backplane`: `value == "v2"`, `backplane_received >= 1`.
- `delete-propagates-to-node-b-and-redis`: `node_b_found == false`, `redis_found == false` (the adapter returns no opaque payload).
- Exit status 0.

## Gotchas

Propagation is polled for up to 3s. Both nodes live in one process; they share no in-memory state, but a true multi-host proof needs two processes on separate machines against the same Redis.

The Redis adapter's versioned storage wrapper is separate from the core L2 envelope. Use adapter `Get`/`Set` for payload observations; direct `prefix+key` access no longer reaches stored data. This proof covers individual-key backplane invalidation, not eviction of remote-only L1 entries by tag ([issue #38](https://github.com/coefficient-engineering/cache/issues/38)).
