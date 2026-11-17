# Cluster-safe tags, recovery, and physical reclamation

## User outcome

Applications can keep payloads sharded across a standard three-primary Redis Cluster while invalidating overlapping tags (including duplicate tags), or clearing tagged and untagged entries. Losing the namespace metadata never makes old payloads valid again: the next write automatically establishes fresh metadata. An application-owned `RunMaintenance(ctx)` loop eventually removes invalidated bytes, even when their Redis TTL is zero (infinite).

This scenario uses two independent native go-redis Cluster clients and the Redis adapter's exported API directly. It deliberately enables `ReadOnly` and `RouteRandomly` on the injected clients. It does not claim remote L1 invalidation or replica/failover coverage.

## How to reach it

Use Bash, Docker, and the repository's Go toolchain. A standalone Redis from the surface README is **not** sufficient. From the repository root, launch the same Redis 7 three-primary topology used by `mise run test:redis-cluster`, with random loopback host ports and translated advertised addresses:

```bash
set -euo pipefail
name=cache-verify-cluster-$$
docker run -d --rm --name "$name" \
  -p 127.0.0.1::7000 -p 127.0.0.1::7001 -p 127.0.0.1::7002 \
  redis:7-alpine sh -ec '
    for port in 7000 7001 7002; do
      mkdir -p "/tmp/redis-$port"
      redis-server --port "$port" --bind 0.0.0.0 --protected-mode no \
        --cluster-enabled yes --cluster-config-file nodes.conf \
        --cluster-node-timeout 5000 --cluster-announce-ip 127.0.0.1 \
        --cluster-announce-port "$port" --cluster-announce-bus-port "$((port + 10000))" \
        --dir "/tmp/redis-$port" --appendonly no --save "" &
    done
    wait
  '
for port in 7000 7001 7002; do
  ready=false
  for ((attempt=0; attempt<100; attempt++)); do
    if [[ "$(docker exec "$name" redis-cli -p "$port" ping 2>/dev/null | tr -d '\r')" == PONG ]]; then
      ready=true
      break
    fi
    sleep 0.1
  done
  test "$ready" = true
done
docker exec "$name" redis-cli --cluster create \
  127.0.0.1:7000 127.0.0.1:7001 127.0.0.1:7002 \
  --cluster-replicas 0 --cluster-yes
for port in 7000 7001 7002; do
  ready=false
  for ((attempt=0; attempt<100; attempt++)); do
    info="$(docker exec "$name" redis-cli -p "$port" cluster info | tr -d '\r')"
    if grep -qx 'cluster_state:ok' <<< "$info" && grep -qx 'cluster_slots_assigned:16384' <<< "$info"; then
      ready=true
      break
    fi
    sleep 0.1
  done
  test "$ready" = true
done
addr0="$(docker port "$name" 7000/tcp)"
addr1="$(docker port "$name" 7001/tcp)"
addr2="$(docker port "$name" 7002/tcp)"
export REDIS_CLUSTER_ADDRS="$addr0,$addr1,$addr2"
export REDIS_CLUSTER_DIAL_MAP="{\"127.0.0.1:7000\":\"$addr0\",\"127.0.0.1:7001\":\"$addr1\",\"127.0.0.1:7002\":\"$addr2\"}"
mkdir -p tmp/verification
```

Doctor, before driving:

```bash
for port in 7000 7001 7002; do
  docker exec "$name" redis-cli -p "$port" cluster info
  docker exec "$name" redis-cli -p "$port" dbsize
done
```

Each primary must report `cluster_state:ok`, `cluster_slots_assigned:16384`, and `dbsize` zero. Teardown handle is `$name`. If launch fails, remove that container before retrying.

For an already reachable disposable three-primary Cluster, supply comma-separated `REDIS_CLUSTER_ADDRS`. `REDIS_CLUSTER_DIAL_MAP` is optional JSON mapping advertised `host:port` addresses to reachable dial addresses; omit it if no translation is needed. The harness rejects missing/empty seed addresses and malformed JSON.

## How to drive it

```bash
mise exec -- go run ./scripts/verify cluster-tags | tee tmp/verification/cluster-tags.jsonl
```

With `pipefail` enabled, exit status zero means every recorded check held. Scenario: `scripts/verify/clustertags.go`. Each run chooses an isolated prefix `verify:cluster-tags:<unixnano>:` and selects three logical payload keys whose Redis hash slots belong to different primaries.

## Proof

Every JSON line must have `"ok":true`:

- `payloads-span-three-primaries`: the selected payload keys span all three actual primaries; injected clients have read routing flags enabled.
- `other-client-reads-opaque-payload`: all six initial binary payloads round-trip unchanged through a second adapter/client, including entries written with duplicate tags.
- `overlapping-duplicate-tags-invalidated`: `DeleteByTags([red, blue, red, blue])` makes all three matching sharded entries miss. `bulk-delete-preserves-other-entries` keeps green-only, unrelated, and untagged values readable.
- `single-tag-invalidates-remaining-match`: `DeleteByTag(green)` removes the remaining match while `single-tag-preserves-unrelated` and `single-tag-preserves-untagged` still return their bytes.
- `clear-invalidates-tagged` and `clear-invalidates-untagged`: `Clear` invalidates both kinds without enumerating payloads in the consumer.
- `before-metadata-loss-readable` and `metadata-hash-loss-injected`: two new records are readable, then a normal Redis `DEL` removes exactly the namespace control hash. `missing-metadata-never-revives-old-data` confirms both tagged and untagged old records miss.
- `fresh-write-automatically-recovers-metadata`: a normal adapter `Set` works after metadata loss with no manual repair. `recovered-metadata-keeps-old-data-invalid` confirms all eight older payloads remain misses.
- `invalidated-ttl-zero-bytes-await-maintenance`: all eight old physical records still have `PTTL == -1`, so expiration cannot explain their later disappearance.
- `maintenance-physically-reclaims-ttl-zero-data`: after starting `RunMaintenance` in an application-owned goroutine, ordinary Redis `EXISTS` reports all eight old records removed within five seconds, including payloads on all three primaries. `maintenance-preserves-fresh-write` still returns the new payload.
- `application-cancels-maintenance-loop`: cancelling the application's maintenance context stops the loop and returns `context.Canceled`.

Physical observations use the documented wire formula, not private adapter helpers: payload key = `prefix + "fc:v2:data:" + base64.RawURLEncoding(key)`; control key = `prefix + "fc:v2:control"`. Each physical Redis command addresses one key, avoiding cross-slot multi-key commands.

## Gotchas

- This intentionally deletes metadata. Use only a disposable Cluster, never shared application data. The random per-run namespace is not a substitute for this prerequisite.
- `ReadOnly`/`RouteRandomly` exercise the injected client's configuration, but the topology has no replicas. This is not a replica consistency or network-fault proof.
- Maintenance is explicit, not a constructor side effect. The scenario uses a 50 ms interval and waits for observable reclamation; application defaults may run more slowly.
- The scenario cancels and joins its maintenance loop, removes its known payload keys and control hash even on failure, and closes both clients. It does not change the integration runner or adapter implementation.

Cleanup, preserving the JSON evidence:

```bash
for port in 7000 7001 7002; do
  docker exec "$name" redis-cli -p "$port" dbsize  # expect 0 on each primary
 done
docker stop "$name"
docker ps -q --filter "name=$name"                 # expect no output
unset REDIS_CLUSTER_ADDRS REDIS_CLUSTER_DIAL_MAP
```

Keep `tmp/verification/cluster-tags.jsonl`. Concurrent runs are safe with separate containers, random host ports, and unique namespaces.
