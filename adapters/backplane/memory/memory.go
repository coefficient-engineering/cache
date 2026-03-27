// Package memory provides an in-process backplane using Go channels.
// Intended for testing multi-node scenarios in a single process.
package memory

import (
	"context"
	"sync"

	"github.com/coefficient-engineering/cache/backplane"
)

// Backplane is an in-process backplane that broadcasts messages via Go channels.
// All subscribers in the same process receive every message.
type Backplane struct {
	mu          sync.RWMutex
	subscribers []chan backplane.Message
	nodeID      string
	closed      bool
}

func New(nodeID string) *Backplane {
	return &Backplane{nodeID: nodeID}
}

func (b *Backplane) Publish(_ context.Context, msg backplane.Message) error {
	msg.SourceID = b.nodeID

	b.mu.RLock()
	defer b.mu.RUnlock()

	if b.closed {
		return nil
	}

	for _, ch := range b.subscribers {
		// Non-blocking send, drop message if subscriber is slow
		select {
		case ch <- msg:
		default:
		}
	}
	return nil
}

func (b *Backplane) Subscribe(handler backplane.Handler) (cancel func(), err error) {
	ch := make(chan backplane.Message, 64)

	b.mu.Lock()
	b.subscribers = append(b.subscribers, ch)
	b.mu.Unlock()

	done := make(chan struct{})

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
		close(done)
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
