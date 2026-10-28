# Stampede protection

## User outcome

50 goroutines calling `GetOrSet` for the same missing key cause exactly one factory call; every caller gets the factory's value.

## How to reach it

Fresh `cache.New()` with defaults; key `product:42` not yet cached.

## How to drive it

```sh
mise exec -- go run ./scripts/verify stampede | tee tmp/verification/stampede.jsonl
```

## Proof

- `factory-called-once`: `ok: true`, `observed.factory_calls == 1` with `callers == 50`.
- `all-callers-got-value`: `ok: true`, `observed == "product-42"`.
- Exit status 0.

## Gotchas

The factory sleeps 100ms so callers overlap; on a heavily loaded machine a caller could arrive after the entry is stored, which still yields one factory call.
