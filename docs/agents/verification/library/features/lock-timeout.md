# Lock timeout proceeds without the lock

The `lock-timeout` consumer scenario holds one factory in flight, then calls `GetOrSet` for the same key with `WithLockTimeout`. The second caller runs its own miss path and returns its factory result before the first factory completes. A second check (`lone-slow-caller-runs-factory-once`) has a lone caller whose factory takes 4× `LockTimeout`: the caller waits for its own factory, which runs exactly once (`factory_calls == 1`, one `EventFactorySuccess`).

Run with `mise exec -- go run ./scripts/verify lock-timeout`.
