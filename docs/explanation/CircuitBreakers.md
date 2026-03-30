# Circuit Breakers

## The problem

When an L2 backend or backplane goes down, every cache operation that touches the failing component encounters a timeout or error. Without protection, the cache keeps retrying the failing component on every request, wasting time and resources.

## How the circuit breaker works

The cache includes a threshold-based circuit breaker for both L2 and the backplane. The circuit breaker tracks consecutive failures and transitions through three states:

### Closed (normal operation)

All calls pass through to the component. The failure counter increments on each error and resets to zero on each success.

### Open (rejecting calls)

When the failure counter reaches the threshold, the circuit breaker opens. All calls are immediately short-circuited:

- L2 reads return `nil` (treated as a cache miss). The cache falls back to the factory.
- L2 writes are skipped.
- Backplane publishes are skipped.

The circuit breaker remains open for the configured duration (`DistributedCacheCircuitBreakerDuration` or `BackplaneCircuitBreakerDuration`).

### Half-open (probing recovery)

After the open duration elapses, the circuit breaker transitions to half-open. The next call is allowed through as a probe:

- If it succeeds, the circuit breaker closes and normal operation resumes. The failure counter is reset.
- If it fails, the circuit breaker re-opens for another duration period.

## State transitions

```
         success
    ┌───────────────┐
    │               │
    ▼               │
 CLOSED ──────► OPEN ──────► HALF-OPEN
   │    threshold    │  duration     │
   │    reached      │  elapsed      │
   │                 │               │
   │                 ◄───────────────┘
   │                    failure (re-open)
   │
   ◄─────────────────────────────────┘
              success (close)
```

## Configuration

L2 circuit breaker:

| Option                                      | Description                                                               |
| ------------------------------------------- | ------------------------------------------------------------------------- |
| `WithL2CircuitBreaker(threshold, duration)` | Threshold: consecutive failures to open. Duration: how long to stay open. |

Backplane circuit breaker:

| Option                                             | Description       |
| -------------------------------------------------- | ----------------- |
| `WithBackplaneCircuitBreaker(threshold, duration)` | Same model as L2. |

A threshold of 0 disables the circuit breaker (the default). With threshold 0, `IsOpen()` always returns false and `Record()` is a no-op.

## Auto-recovery

When the backplane circuit breaker transitions from open/half-open to closed (recovery), the cache clears L1 and the tag index. This is controlled by the `BackplaneAutoRecovery` option (default: true).

The rationale: while the backplane was down, other nodes may have written new values. The local L1 could be stale. Clearing L1 forces the next requests to read from L2, ensuring consistency.

The `onRecovery` callback is invoked in a separate goroutine to avoid blocking the operation that triggered the recovery.

## Events

The circuit breaker emits events on state changes:

- `EventL2CircuitBreakerStateChange{Open: true}` when checked and found open.
- `EventBackplaneCircuitBreakerStateChange{Open: true}` when checked and found open.

## Interaction with fail-safe

When the L2 circuit breaker is open, L2 reads return nil. If fail-safe is enabled and L1 has a stale entry, the factory is called. If the factory also fails, the stale value is returned. The circuit breaker prevents the slow-timeout path through L2, reducing latency during outages.

## See also

- [Using Circuit Breakers](../how-to/UsingCircuitBreakers.md) for configuration steps
