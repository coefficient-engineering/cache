# Using Circuit Breakers

Circuit breakers prevent the cache from repeatedly calling a failing L2 backend or backplane. After a threshold of consecutive failures, the circuit opens and all calls to that component are skipped for a configured duration.

## Configure the L2 circuit breaker

```go
c, err := cache.New(
    cache.WithL2(l2Adapter),
    cache.WithSerializer(serializer),
    cache.WithL2CircuitBreaker(5, 10*time.Second),
)
```

After 5 consecutive L2 errors, the circuit opens. For the next 10 seconds, all L2 reads return `nil` (treated as a cache miss) and all L2 writes are skipped. The cache falls back to L1 and the factory.

## Configure the backplane circuit breaker

```go
c, err := cache.New(
    cache.WithBackplane(bpAdapter),
    cache.WithBackplaneCircuitBreaker(3, 15*time.Second),
)
```

After 3 consecutive backplane publish errors, the circuit opens. For the next 15 seconds, all backplane publishes are skipped.

## States

The circuit breaker has three states:

| State     | Behavior                                                                                                                               |
| --------- | -------------------------------------------------------------------------------------------------------------------------------------- |
| Closed    | Normal operation. Calls pass through to the component.                                                                                 |
| Open      | All calls are skipped. Entered after `threshold` consecutive failures.                                                                 |
| Half-open | After `openDuration` elapses, one probe call is allowed through. If it succeeds, the circuit closes. If it fails, the circuit reopens. |

## Recovery behavior

When the backplane circuit breaker transitions from open/half-open to closed (a successful probe), the cache automatically clears L1 and the tag index. This is because backplane messages may have been missed during the outage, leaving L1 potentially stale.

This auto-recovery behavior is controlled by `BackplaneAutoRecovery` on the `Options` struct (default: `true`).

## Disabling circuit breakers

Set the threshold to 0 (the default) to disable:

```go
cache.WithL2CircuitBreaker(0, 0)          // L2 circuit breaker disabled
cache.WithBackplaneCircuitBreaker(0, 0)   // backplane circuit breaker disabled
```

When disabled, `IsOpen()` always returns `false` and `Record()` is a no-op.

## Events

The cache emits events when circuit breaker state changes:

- `EventL2CircuitBreakerStateChange{Open: true}` when the L2 circuit opens
- `EventBackplaneCircuitBreakerStateChange{Open: true}` when the backplane circuit opens

Subscribe to these via `c.Events().On(...)`. See [Observing Events](ObservingEvents.md).

## See also

- [Circuit Breakers](../explanation/CircuitBreakers.md) for the design rationale and state machine details
