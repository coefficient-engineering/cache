# Tag invalidation

## User outcome

`DeleteByTag("featured")` removes `p:1` and `p:2` (tagged `featured`) and leaves `p:3` (tagged `clearance`).

## How to reach it

Fresh cache; three `Set` calls with `WithTags`.

## How to drive it

```sh
mise exec -- go run ./scripts/verify tags | tee tmp/verification/tags.jsonl
```

## Proof

- `tagged-entries-removed`: `observed == {"p:1":false,"p:2":false,"p:3":true}`.
- `untagged-entry-kept`: same map, `p:3` true.
- Exit status 0.

## Gotchas

L1 only here; tag invalidation through L2 is the Redis adapter's job (see redis-multinode).
