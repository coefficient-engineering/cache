# Lock timeout proceeds without the lock

The `lock-timeout` consumer scenario holds one factory in flight, then calls `GetOrSet` for the same key with `WithLockTimeout`. The second caller runs its own miss path and returns its factory result before the first factory completes.

Run with `mise exec -- go run ./scripts/verify lock-timeout`.
