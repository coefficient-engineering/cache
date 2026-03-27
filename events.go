package cache

import (
	"sync"

	"github.com/coefficient-engineering/cache/backplane"
)

// Event is the marker interface for all cache events.
type Event interface {
	cacheEvent()
}

type (
	EventCacheHit struct {
		Key     string
		IsStale bool
	}
	EventCacheMiss      struct{ Key string }
	EventFactoryCall    struct{ Key string }
	EventFactorySuccess struct {
		Key    string
		Shared bool
	}
	EventFactoryError struct {
		Key string
		Err error
	}
	EventSoftTimeoutActivated struct{ Key string }
	EventHardTimeoutActivated struct{ Key string }
	EventFailSafeActivated    struct {
		Key        string
		StaleValue any
	}
	EventEagerRefreshStarted  struct{ Key string }
	EventEagerRefreshComplete struct{ Key string }
	EventL2Hit                struct{ Key string }
	EventL2Miss               struct{ Key string }
	EventL2Error              struct {
		Key string
		Err error
	}
	EventL2CircuitBreakerStateChange struct{ Open bool }
	EventBackplaneSent               struct {
		Key  string
		Type backplane.MessageType
	}
	EventBackplaneReceived struct {
		Key  string
		Type backplane.MessageType
	}
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
type EventHandler func(Event)

// EventEmitter is a simple in-process event bus. Safe for concurrent use.
type EventEmitter struct {
	mu       sync.RWMutex
	handlers []*eventHandlerSlot
}

type eventHandlerSlot struct {
	fn EventHandler
}

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
