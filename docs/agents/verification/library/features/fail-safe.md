# Fail-safe

## User outcome

With fail-safe enabled, an expired entry whose refresh fails is served stale instead of surfacing the error; with no stale value the error propagates.

## How to reach it

`Set` `price:7` with `WithDuration(50ms)` and `WithFailSafe(1h, 1s)`, wait for logical expiry, then `GetOrSet` with a factory returning an error.

## How to drive it

```sh
mise exec -- go run ./scripts/verify failsafe | tee tmp/verification/failsafe.jsonl
```

## Proof

- `stale-value-returned`: `observed.value == "9.99"`, `observed.error == ""`.
- `failsafe-event-emitted`: exactly one `EventFailSafeActivated`.
- `no-stale-propagates-error`: a never-cached key returns `upstream down`.
- Exit status 0.

## Gotchas

Timing-based (50ms duration, 100ms wait). Throttle re-promotion means a second failing call within 1s is served from L1 without calling the factory.
