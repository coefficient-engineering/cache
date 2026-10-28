# Backplane publish errors on mutations

## User outcome

With `AllowBackgroundBackplaneOperations: false` and `ReThrowBackplaneExceptions: true`, a failed backplane publish is returned from `Set` / `Delete` (wrapping the transport error), while the local write is kept. With default options the failure is only logged.

## How to reach it

Launch and Doctor from the surface README; `REDIS_ADDR` exported. The scenario subscribes a Redis backplane, then closes that backplane's Redis client so every publish fails inside go-redis (`redis.ErrClosed`).

## How to drive it

```sh
mise exec -- go run ./scripts/verify backplane-rethrow | tee tmp/verification/backplane-rethrow.jsonl
```

## Proof

- `default-set-swallows-publish-failure`: `observed == ""`.
- `sync-rethrow-set-returns-publish-error`: `observed` is `cache: backplane publish (type 1, key "k"): redis: client is closed`.
- `local-write-kept-after-failed-publish`: `value == "rethrown"`, `found == true`.
- `sync-rethrow-delete-returns-publish-error`: type 2, same cause.
- Exit status 0.

## Gotchas

Writes nothing to Redis (no L2 configured), so `dbsize` stays 0. The backplane circuit breaker is disabled by default; with it open, publishes are skipped and no error is returned.
