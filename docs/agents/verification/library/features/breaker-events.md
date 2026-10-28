# Circuit-breaker transition events

The `breaker-events` consumer scenario exercises failing then recovering L2 and backplane adapters through the public API. Each breaker emits exactly one open event and one closed event after a successful half-open probe.

Run with `mise exec -- go run ./scripts/verify breaker-events`.
