# L2 soft timeout

## User outcome

With fail-safe on and a stale L1 entry, `GetOrSet` with `DistributedCacheSoftTimeout` stops waiting for a slow L2 read after the soft timeout and calls the factory: a failing factory serves the stale value, a working one returns its fresh value, both in about the soft timeout instead of the L2 latency. Without a fail-safe fallback, `GetOrSet` waits for L2.

## How to reach it

Launch and Doctor from the surface README; `REDIS_ADDR` exported. The scenario adds a go-redis hook that delays every `GET` by 1s once enabled, so the Redis L2 really is slow. Entries have `Duration` 200ms and are made stale by sleeping.

## How to drive it

```sh
mise exec -- go run ./scripts/verify l2-soft-timeout | tee tmp/verification/l2-soft-timeout.jsonl
```

## Proof

- `soft-timeout-serves-stale-without-waiting-for-l2`: `value == "old"`, `elapsed` ≈ 50ms (< 500ms).
- `soft-timeout-calls-factory-without-waiting-for-l2`: `value == "fresh"`, `factoryCalls == 1`, `elapsed` ≈ 50ms.
- `no-fallback-waits-for-slow-l2`: `elapsed` ≥ 1s.
- Exit status 0; `dbsize` back to 0.

## Gotchas

Timing-based: on a heavily loaded machine the 500ms bound could be missed. `Get` and L2 writes ignore the soft timeout.
