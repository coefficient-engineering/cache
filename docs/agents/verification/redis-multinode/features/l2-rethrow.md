# L2 and serialization errors reaching callers

## User outcome

With a Redis L2 that fails every call, default options keep working on L1 only: `Set`/`Get`/`GetOrSet` return no error and an `EventL2Error` is emitted. With `ReThrowDistributedCacheExceptions` (per call, or cache-wide via `Options.SkipL2OnError: false`), the failure is returned from `Set`, `Get`, `GetOrSet` (read side, factory not called), `Delete`, `DeleteByTag` and `Clear`, wrapping the go-redis error; a background L2 write and a per-call opt-out stay silent. With the default `ReThrowSerializationExceptions: true`, an unencodable value fails `Set` and a corrupt stored envelope fails `Get`/`GetOrSet`; turning it off logs the write failure and treats the corrupt entry as a miss that the factory overwrites.

## How to reach it

Launch and Doctor from the surface README; `REDIS_ADDR` exported. The broken-L2 half builds a cache over `adapters/l2/redis` and closes that adapter's Redis client, so every L2 call fails inside go-redis (`redis.ErrClosed`). The serialization half uses a live client and writes `not an envelope` straight into Redis under the cache's key.

## How to drive it

```sh
mise exec -- go run ./scripts/verify l2-rethrow | tee tmp/verification/l2-rethrow.jsonl
```

## Proof

- `default-set-swallows-l2-failure`, `default-get-is-clean-miss`, `rethrow-background-set-swallows-l2-error`, `skipl2onerror-false-per-call-opt-out`: `observed == ""`.
- `local-write-kept-after-failed-l2-write`: `value == "default"`, `found == true`; `l2-error-event-emitted`: `1`.
- `rethrow-set-returns-l2-error`: `cache: L2 write (key "k"): redis: client is closed`; the `get`, `getorset` (`factoryCalls == 0`), `delete`, `delete-by-tag` and `skipl2onerror-false-*` checks show the matching `cache: L2 <op> ...: redis: client is closed`.
- `default-set-returns-serialization-error`: `cache: L2 serialization (key "chan"): value: json: unsupported type: chan int`; `no-rethrow-set-logs-serialization-error`: `error == ""`, `logLines == 2`.
- `default-get-returns-deserialization-error` and `default-getorset-returns-deserialization-error-without-factory` (`factoryCalls == 0`): `cache: L2 deserialization (key "bad"): envelope: invalid character ...`.
- `no-rethrow-getorset-treats-corrupt-entry-as-miss`: `value == "fresh"`, `factoryCalls == 1`, `redisValue` is a JSON envelope (`"le"` present).
- Exit status 0; `dbsize` back to 0 afterwards.

## Gotchas

The L2 circuit breaker is disabled by default; with it open, `Set`/`Get`/`GetOrSet` skip L2 and return no error. `Clear` has no per-call options, so it is driven through the `SkipL2OnError: false` cache.
