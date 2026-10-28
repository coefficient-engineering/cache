# Expire reaches the shared L2

## User outcome

After `Expire`, the next `GetOrSet` on any node calls the factory instead of returning the old value from L2. With fail-safe on, the Redis envelope is rewritten with logical expiry = now and its original physical expiry (TTL kept), so another node whose factory fails still gets the old value. With fail-safe off, the Redis key is deleted.

## How to reach it

Launch and Doctor from the surface README; `REDIS_ADDR` exported. Two cache nodes share `adapters/l2/redis` under a per-run key prefix (no backplane, so only L2 carries the expiry).

## How to drive it

```sh
mise exec -- go run ./scripts/verify expire-l2 | tee tmp/verification/expire-l2.jsonl
```

## Proof

- `failsafe-expire-rewrites-redis-envelope`: `le` ≤ now, `pe` about 1h ahead, `ttlAfter` ≈ `ttlBefore` (1h).
- `other-node-gets-l2-fail-safe-fallback`: `value == "old"` with a failing factory.
- `same-node-getorset-calls-factory-after-expire` and `other-node-getorset-calls-factory-after-expire`: `value == "fresh"`, `factoryCalls == 1`.
- `no-failsafe-expire-deletes-redis-key`: `exists == 0`.
- Exit status 0; `dbsize` back to 0.

## Gotchas

Whether fail-safe applies is decided by the `Expire` call's entry options, not by the options the entry was written with.
