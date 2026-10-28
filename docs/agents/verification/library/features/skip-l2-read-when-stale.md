# Skip L2 reads when L1 is stale

The `skip-l2-read-when-stale` consumer scenario sets up a stale L1 entry and a newer fresh L2 value. Without `WithSkipL2ReadWhenStale`, GetOrSet returns the L2 value; with it, GetOrSet skips L2, calls the factory, and emits no `EventL2Hit`.

Run with `mise exec -- go run ./scripts/verify skip-l2-read-when-stale`.
