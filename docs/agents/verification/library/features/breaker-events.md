# Circuit-breaker transitions and backplane recovery

The `breaker-events` consumer scenario exercises failing then recovering L2 and backplane adapters through the public API. Each breaker emits exactly one open event and one closed event after a successful half-open probe; successful backplane recovery clears the L1 entries.

Run with `mise exec -- go run ./scripts/verify breaker-events`.
