# Observing Events

The cache emits events for cache hits, misses, factory calls, timeouts, fail-safe activations, L2 operations, and backplane messages. Subscribe to these events to wire up metrics, logging, or other observability.

## Subscribe to events

```go
unsubscribe := c.Events().On(func(e cache.Event) {
    switch evt := e.(type) {
    case cache.EventCacheHit:
        fmt.Printf("hit: key=%s stale=%v\n", evt.Key, evt.IsStale)
    case cache.EventCacheMiss:
        fmt.Printf("miss: key=%s\n", evt.Key)
    case cache.EventFactoryError:
        fmt.Printf("factory error: key=%s err=%v\n", evt.Key, evt.Err)
    }
})

// Later, when you no longer need the subscription:
unsubscribe()
```

`Events()` returns an `*EventEmitter`. The `On` method registers a handler and returns an unsubscribe function.

## Handler execution

Handlers are called synchronously on the goroutine that produced the event. Keep handlers fast. If you need to do slow work (HTTP calls, database writes), send the event to a channel or goroutine from within the handler.

```go
eventCh := make(chan cache.Event, 100)

c.Events().On(func(e cache.Event) {
    select {
    case eventCh <- e:
    default:
        // drop if channel is full
    }
})

go func() {
    for e := range eventCh {
        // process events asynchronously
        _ = e
    }
}()
```

## Example: Prometheus metrics

```go
c.Events().On(func(e cache.Event) {
    switch e.(type) {
    case cache.EventCacheHit:
        cacheHitsTotal.Inc()
    case cache.EventCacheMiss:
        cacheMissesTotal.Inc()
    case cache.EventFailSafeActivated:
        failSafeActivationsTotal.Inc()
    case cache.EventL2Error:
        l2ErrorsTotal.Inc()
    case cache.EventFactoryCall:
        factoryCallsTotal.Inc()
    }
})
```

## All event types

| Type                                      | Fields                                     | When emitted                                        |
| ----------------------------------------- | ------------------------------------------ | --------------------------------------------------- |
| `EventCacheHit`                           | `Key string`, `IsStale bool`               | L1 hit (fresh or stale with `AllowStaleOnReadOnly`) |
| `EventCacheMiss`                          | `Key string`                               | No fresh entry in L1                                |
| `EventFactoryCall`                        | `Key string`                               | Factory is about to be called                       |
| `EventFactorySuccess`                     | `Key string`, `Shared bool`                | Factory returned successfully                       |
| `EventFactoryError`                       | `Key string`, `Err error`                  | Factory returned an error                           |
| `EventSoftTimeoutActivated`               | `Key string`                               | Soft timeout fired, returning stale value           |
| `EventHardTimeoutActivated`               | `Key string`                               | Hard timeout fired                                  |
| `EventFailSafeActivated`                  | `Key string`, `StaleValue any`             | Stale value returned due to factory failure         |
| `EventEagerRefreshStarted`                | `Key string`                               | Background eager refresh goroutine started          |
| `EventEagerRefreshComplete`               | `Key string`                               | Background eager refresh succeeded                  |
| `EventL2Hit`                              | `Key string`                               | Fresh value found in L2                             |
| `EventL2Miss`                             | `Key string`                               | No value (or only stale value) in L2                |
| `EventL2Error`                            | `Key string`, `Err error`                  | L2 operation failed                                 |
| `EventL2CircuitBreakerStateChange`        | `Open bool`                                | L2 circuit breaker state changed                    |
| `EventBackplaneSent`                      | `Key string`, `Type backplane.MessageType` | Message published to backplane                      |
| `EventBackplaneReceived`                  | `Key string`, `Type backplane.MessageType` | Message received from backplane                     |
| `EventBackplaneCircuitBreakerStateChange` | `Open bool`                                | Backplane circuit breaker state changed             |

## Multiple handlers

You can register multiple handlers. Each receives every event:

```go
c.Events().On(metricsHandler)
c.Events().On(loggingHandler)
```
