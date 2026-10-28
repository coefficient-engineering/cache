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

- `l2-key-written-to-redis`: `observed.exists == 1` for `verify:<run>:sku:1` (read straight from Redis).
- `node-b-reads-node-a-write-via-l2`: `value == "v1"`, `l2_hits == 1`.
- `node-b-sees-update-via-backplane`: `value == "v2"`, `backplane_received >= 1`.
- `delete-propagates-to-node-b-and-redis`: `node_b_found == false`, `redis_exists == 0`.
- Exit status 0.

## Gotchas

Propagation is polled for up to 3s. Both nodes live in one process; they share no in-memory state, but a true multi-host proof needs two processes on separate machines against the same Redis.
