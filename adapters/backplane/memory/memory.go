// Package memory provides an in-process backplane using Go channels.
// Intended for testing multi-node scenarios in a single process.
package memory

import (
	"context"
	"sync"

	"github.com/coefficient-engineering/cache/backplane"
)

// Hub connects multiple in-process Backplane instances so that a message
// published by one is delivered to all others. Use NewHub to create a hub,
// then NewWithHub to create backplanes that share it.
type Hub struct {
	mu         sync.RWMutex
	backplanes []*Backplane
}

// NewHub creates a shared hub for connecting multiple in-process backplanes.
func NewHub() *Hub {
	return &Hub{}
}

func (h *Hub) register(bp *Backplane) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.backplanes = append(h.backplanes, bp)
}

// broadcast delivers msg to all backplanes in the hub except the sender.
func (h *Hub) broadcast(msg backplane.Message) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, bp := range h.backplanes {
		bp.deliver(msg)
	}
}

// Backplane is an in-process backplane that broadcasts messages via Go channels.
// All subscribers in the same process receive every message.
type Backplane struct {
	mu          sync.RWMutex
	subscribers []chan backplane.Message
	nodeID      string
	closed      bool
	hub         *Hub
}

// New creates a standalone Backplane that only delivers to its own subscribers.
func New(nodeID string) *Backplane {
	return &Backplane{nodeID: nodeID}
}

// NewWithHub creates a Backplane connected to hub so that published messages
// are delivered to all other backplanes registered on the same hub.
func NewWithHub(nodeID string, hub *Hub) *Backplane {
	bp := &Backplane{nodeID: nodeID, hub: hub}
	hub.register(bp)
	return bp
}

// deliver sends msg to this backplane's local subscribers (used by Hub).
func (b *Backplane) deliver(msg backplane.Message) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	if b.closed {
		return
	}
	for _, ch := range b.subscribers {
		select {
		case ch <- msg:
		default:
		}
	}
}

func (b *Backplane) Publish(_ context.Context, msg backplane.Message) error {
	msg.SourceID = b.nodeID

	// Deliver to own subscribers.
	b.deliver(msg)

	// If part of a hub, broadcast to all other backplanes too.
	if b.hub != nil {
		b.hub.broadcast(msg)
	}

	return nil
}

func (b *Backplane) Subscribe(handler backplane.Handler) (cancel func(), err error) {
	ch := make(chan backplane.Message, 64)

	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return func() {}, nil
	}
	b.subscribers = append(b.subscribers, ch)
	b.mu.Unlock()

	done := make(chan struct{})
	var once sync.Once
	// Handler runs on a managed goroutine, never on the caller's goroutine
	go func() {
		for {
			select {
			case msg, ok := <-ch:
				if !ok {
					return
				}
				// Skip self-messages
				if msg.SourceID == b.nodeID {
					continue
				}
				handler(msg)
			case <-done:
				return
			}
		}
	}()

	cancel = func() {
		once.Do(func() {
			close(done)
			b.mu.Lock()
			defer b.mu.Unlock()
			for i, sub := range b.subscribers {
				if sub == ch {
					b.subscribers = append(b.subscribers[:i], b.subscribers[i+1:]...)
					break
				}
			}
		})
	}
	return cancel, nil
}

func (b *Backplane) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.closed = true
	for _, ch := range b.subscribers {
		close(ch)
	}
	b.subscribers = nil
	return nil
}

var _ backplane.Backplane = (*Backplane)(nil)
