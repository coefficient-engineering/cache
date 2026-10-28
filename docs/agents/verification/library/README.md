# Surface: library

## Surface

The core module `github.com/coefficient-engineering/cache` (root package plus `adapters/l1/syncmap`, `adapters/l2/memory`, `adapters/backplane/memory`, `adapters/serializer/json`), consumed through `cache.New()` and the generic helpers `cache.GetOrSet[T]` / `cache.Get[T]`. No external services.

## Launch

Nothing long-running. From the repo root, with the pinned toolchain:

```sh
mise install
mkdir -p tmp/verification
```

Each scenario is a short-lived process: `mise exec -- go run ./scripts/verify <scenario>`.

## Doctor

```sh
mise exec -- go build ./scripts/verify && rm -f verify && echo DOCTOR_OK
```

`DOCTOR_OK` means the consumer module compiles against the working-tree library through `go.work`. If this fails, the library itself is broken; stop and report INCONCLUSIVE with the compiler output.

## Drive

```sh
mise exec -- go run ./scripts/verify <stampede|failsafe|tags> | tee tmp/verification/<scenario>.jsonl
echo "exit ${PIPESTATUS[0]}"
```

Scenarios live in `scripts/verify/l1.go`; `scripts/verify/main.go` registers them. To prove a new capability, add a scenario that uses only exported API and records observable results with `r.record(check{...})`.

## Evidence

- Each JSON line: `scenario`, `check`, `ok`, `observed` (the values actually seen). Keep the `.jsonl` file and the exit code.
- Event-based checks attach a handler with `Cache.Events().On`, the same public hook applications use for metrics.
- No secrets are produced; nothing to redact.

## Cleanup

Each scenario closes its cache before exiting; no processes, ports, or files survive except the evidence in `tmp/verification/` (gitignored; keep it). Remove a stray build output with `rm -f verify`.

## Isolation

Fully isolated: in-process caches, no shared state. Any number of scenarios can run concurrently.

## Feature map

See [features/README.md](features/README.md). Every user-facing capability of the core API belongs there.
