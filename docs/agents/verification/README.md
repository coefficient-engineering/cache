# Verification

How to prove behaviour on the surfaces a consumer of this library actually touches. Tests and `mise run check` support a proof; they don't replace one.

| Surface | Choose when the change touches |
| --- | --- |
| [library](library/README.md) | The core API in a single process: `GetOrSet`/`Get`/`Set`/`Delete`/`DeleteByTag`, stampede protection, fail-safe, timeouts, tags, events, L1 adapters. |
| [redis-multinode](redis-multinode/README.md) | Anything that crosses nodes: L2 reads/writes, the envelope/serializer, the backplane, the Redis adapters (`adapters/*/redis`). |

Both surfaces drive the library through `scripts/verify`, an unpublished consumer module in `go.work` that imports the packages by their public module paths, as an application does. Each scenario prints one JSON line per check and exits 0 only when every check holds.

Verdicts: **VERIFIED** (every check `ok: true`, exit 0), **NOT VERIFIED** (a check `ok: false` / exit 1), **INCONCLUSIVE** (scenario couldn't run, e.g. Doctor failed).
