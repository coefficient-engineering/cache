# Surface: redis-multinode

## Surface

Two cache nodes (distinct `WithNodeID`) in one consumer process, each with its own Redis connection, sharing L2 through `adapters/l2/redis` and invalidating each other through `adapters/backplane/redis`. Nodes communicate only through a real Redis server.

## Launch

Requires Docker. Start a disposable Redis on a random loopback port:

```sh
name=cache-verify-redis-$$
docker run -d --rm --name "$name" -p 127.0.0.1::6379 redis:7-alpine
export REDIS_ADDR="127.0.0.1:$(docker port "$name" 6379/tcp | head -1 | sed 's/.*://')"
mkdir -p tmp/verification
```

Readiness: Doctor below. Teardown handle: the container name in `$name`.

## Doctor

```sh
docker exec "$name" redis-cli ping && docker exec "$name" redis-cli dbsize
```

Expect `PONG`, then `0` (empty instance; a non-empty one is not this run's container). Retry `ping` for a few seconds right after launch. Also run the library surface's Doctor so the consumer module compiles.

## Drive

```sh
mise exec -- go run ./scripts/verify multinode | tee tmp/verification/multinode.jsonl
echo "exit ${PIPESTATUS[0]}"
```

Scenario code: `scripts/verify/multinode.go`. Every key and the backplane channel are namespaced with a per-run prefix `verify:<unixnano>:`.

## Evidence

- JSON lines as for the library surface; `observed` includes the logical L2 key, opaque payload byte count, L2 hit and backplane-received event counts.
- Durable side effects are checked through the public Redis adapter's `Get`, bypassing cache L1 and core decoding. Do not assume a raw Redis data key equals `runID+key`: the adapter uses versioned keys and wraps the opaque core envelope. Run-prefix enumeration is used only for cleanup.
- The disposable Redis has no password; never point `REDIS_ADDR` at a shared instance with credentials in the URL.
- Both Redis adapter constructors must succeed before creating the cache nodes. This standalone surface does not establish Cluster tag ordering or metadata-loss recovery coverage.
- The finite-lived scenario explicitly deletes its data and run-prefixed metadata. Long-running applications instead own and cancel `RunMaintenance(ctx)` for eventual reclamation after tag invalidation/Clear, including TTL-zero data; see [Redis lifecycle](../../../how-to/AddingDistributedL2.md#redis-invalidation-and-lifecycle).

## Cleanup

```sh
docker exec "$name" redis-cli dbsize   # expect 0: the scenario deletes its run-prefixed keys
docker stop "$name"                     # --rm removes the container
docker ps -q --filter "name=$name"      # expect no output
```

Keep `tmp/verification/multinode.jsonl`.

## Isolation

Safe to run concurrently: each run uses its own container (`$$` in the name, random host port) and a unique key/channel prefix, so runs against a shared Redis also don't collide.

## Feature map

See [features/README.md](features/README.md). Every cross-node capability belongs there.
