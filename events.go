package cache

import (
	"sync"

	"github.com/coefficient-engineering/cache/backplane"
)

// Event is the marker interface for all cache events.
// Use a type switch to handle specific event types.
//
//	cache.Events().On(func(e cache.Event) {
//	    switch ev := e.(type) {
//	    case cache.EventCacheHit:
//	        hits.Inc()
//	    case cache.EventCacheMiss:
//	        misses.Inc()
//	    }
//	})
type Event interface {
	cacheEvent()
}

// Cache hit/miss events.
type (
	// EventCacheHit is emitted on an L1 hit (fresh or stale with
	// [EntryOptions.AllowStaleOnReadOnly]).
	EventCacheHit struct {
		Key     string
		IsStale bool
	}
	// EventCacheMiss is emitted when no fresh L1 entry is found.
	EventCacheMiss struct{ Key string }
)

// Factory events.
type (
	// EventFactoryCall is emitted when the factory is about to be called.
	EventFactoryCall struct{ Key string }
	// EventFactorySuccess is emitted when the factory returns successfully.
	// Shared indicates the result came from singleflight (another goroutine's call).
	EventFactorySuccess struct {
		Key    string
		Shared bool
	}
	// EventFactoryError is emitted when the factory returns an error
	// (after fail-safe evaluation).
	EventFactoryError struct {
		Key string
		Err error
	}
)

// Timeout events.
type (
	// EventSoftTimeoutActivated is emitted when the factory soft timeout
	// fires and a stale value is returned.
	EventSoftTimeoutActivated struct{ Key string }
	// EventHardTimeoutActivated is emitted when the factory hard timeout fires.
	EventHardTimeoutActivated struct{ Key string }
)

// Fail-safe events.
type (
	// EventFailSafeActivated is emitted when a stale value is returned
	// due to a factory or L2 failure.
	EventFailSafeActivated struct {
		Key        string
		StaleValue any
	}
)

// Eager refresh events.
type (
	// EventEagerRefreshStarted is emitted when a background eager refresh
	// goroutine starts.
	EventEagerRefreshStarted struct{ Key string }
	// EventEagerRefreshComplete is emitted when a background eager refresh
	// goroutine completes.
	EventEagerRefreshComplete struct{ Key string }
)

// L2 events.
type (
	// EventL2Hit is emitted when L2 returns a fresh entry.
	EventL2Hit struct{ Key string }
	// EventL2Miss is emitted when L2 does not have the key or has a stale entry.
	EventL2Miss struct{ Key string }
	// EventL2Error is emitted when an L2 read or write fails.
	EventL2Error struct {
		Key string
		Err error
	}
	// EventL2CircuitBreakerStateChange is emitted when the L2 circuit
	// breaker opens or closes.
	EventL2CircuitBreakerStateChange struct{ Open bool }
)

// Backplane events.
type (
	// EventBackplaneSent is emitted when a backplane message is published
	// successfully.
	EventBackplaneSent struct {
		Key  string
		Type backplane.MessageType
	}
	// EventBackplaneReceived is emitted when a backplane message is
	// received from another node.
	EventBackplaneReceived struct {
		Key  string
		Type backplane.MessageType
	}
	// EventBackplaneCircuitBreakerStateChange is emitted when the backplane
	// circuit breaker opens or closes.
	EventBackplaneCircuitBreakerStateChange struct{ Open bool }
)

func (EventCacheHit) cacheEvent()                           {}
func (EventCacheMiss) cacheEvent()                          {}
func (EventFactoryCall) cacheEvent()                        {}
func (EventFactorySuccess) cacheEvent()                     {}
func (EventFactoryError) cacheEvent()                       {}
func (EventSoftTimeoutActivated) cacheEvent()               {}
func (EventHardTimeoutActivated) cacheEvent()               {}
func (EventFailSafeActivated) cacheEvent()                  {}
func (EventEagerRefreshStarted) cacheEvent()                {}
func (EventEagerRefreshComplete) cacheEvent()               {}
func (EventL2Hit) cacheEvent()                              {}
func (EventL2Miss) cacheEvent()                             {}
func (EventL2Error) cacheEvent()                            {}
func (EventL2CircuitBreakerStateChange) cacheEvent()        {}
func (EventBackplaneSent) cacheEvent()                      {}
func (EventBackplaneReceived) cacheEvent()                  {}
func (EventBackplaneCircuitBreakerStateChange) cacheEvent() {}

// EventHandler is a callback invoked for each cache event.
// Handlers are called synchronously on the goroutine that produced the event.
// Keep handlers fast; offload expensive work to a channel or goroutine.
type EventHandler func(Event)

// EventEmitter is a simple in-process event bus. Safe for concurrent use.
//
// Obtain a reference via [Cache.Events]. Register handlers with [EventEmitter.On].
type EventEmitter struct {
	mu       sync.RWMutex
	handlers []*eventHandlerSlot
}

type eventHandlerSlot struct {
	fn EventHandler
}

// On registers handler to receive all cache events and returns an
// unsubscribe function that removes the handler.
//
// Handlers are called synchronously on the goroutine that produced the event.
// Keep handlers fast; offload expensive work to a channel or goroutine.
func (e *EventEmitter) On(handler EventHandler) (unsubscribe func()) {
	slot := &eventHandlerSlot{fn: handler}
	e.mu.Lock()
	e.handlers = append(e.handlers, slot)
	e.mu.Unlock()
	return func() {
		e.mu.Lock()
		slot.fn = nil // nil out; slot stays until next compact
		e.mu.Unlock()
	}
}

func (e *EventEmitter) emit(event Event) {
	e.mu.RLock()
	handlers := make([]EventHandler, 0, len(e.handlers))
	for _, slot := range e.handlers {
		if fn := slot.fn; fn != nil {
			handlers = append(handlers, fn)
		}
	}
	e.mu.RUnlock()

	for _, fn := range handlers {
		fn(event)
	}
}
