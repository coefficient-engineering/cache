# Shared factory-success events

The `shared-factory-events` consumer scenario calls `GetOrSet` concurrently for one missing key and observes lifecycle events through the public API. Exactly one `EventFactorySuccess` has `Shared: false`; each other caller that receives that factory result has `Shared: true`.

Run with `mise exec -- go run ./scripts/verify shared-factory-events`.
