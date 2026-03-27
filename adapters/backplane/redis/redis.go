// Package redis is an implementation of the Backplane interface using
// Redis pub/sub via the go-redis client library.
//
// It requires *redis.Client (not redis.Cmdable) because pub/sub is only
// available on a dedicated client connection. Messages are JSON-encoded
// backplane.Message values published to a configurable channel.
package redis

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/coefficient-engineering/cache/backplane"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// Option configures a Backplane.
type Option func(*Backplane)

// WithChannel sets the Redis pub/sub channel name. Default: "cache:backplane".
func WithChannel(channel string) Option {
	return func(b *Backplane) { b.channel = channel }
}

// WithNodeID sets the node identifier used to filter self-messages.
// If not set, a random UUID is generated.
func WithNodeID(id string) Option {
	return func(b *Backplane) { b.nodeID = id }
}

// WithLogger sets a structured logger for diagnostic output.
func WithLogger(logger *slog.Logger) Option {
	return func(b *Backplane) { b.logger = logger }
}

// Backplane implements backplane.Backplane using Redis pub/sub.
type Backplane struct {
	client  *redis.Client
	channel string
	nodeID  string
	logger  *slog.Logger

	mu     sync.Mutex
	closed bool
	done   chan struct{}
}

// New creates a Redis-backed backplane.
//
// client must be a *redis.Client (pub/sub requires a dedicated connection).
func New(client *redis.Client, opts ...Option) *Backplane {
	b := &Backplane{
		client:  client,
		channel: "cache:backplane",
		nodeID:  uuid.NewString(),
		logger:  slog.New(slog.NewTextHandler(io.Discard, nil)), done: make(chan struct{}),
	}
	for _, opt := range opts {
		opt(b)
	}
	return b
}

func (b *Backplane) Publish(ctx context.Context, msg backplane.Message) error {
	if msg.SourceID == "" {
		msg.SourceID = b.nodeID
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	return b.client.Publish(ctx, b.channel, data).Err()
}

func (b *Backplane) Subscribe(handler backplane.Handler) (cancel func(), err error) {
	pubsub := b.client.Subscribe(context.Background(), b.channel)

	// Wait for subscription confirmation.
	receiveCtx, cancelReceive := context.WithCancel(context.Background())
	defer cancelReceive()

	watchDone := make(chan struct{})
	go func() {
		select {
		case <-b.done:
			cancelReceive()
		case <-watchDone:
		}
	}()
	defer close(watchDone)

	receiveTimeoutCtx, cancelTimeout := context.WithTimeout(receiveCtx, 5*time.Second)
	defer cancelTimeout()

	if _, err := pubsub.Receive(receiveTimeoutCtx); err != nil {
		_ = pubsub.Close()
		return nil, err
	}

	ch := pubsub.Channel()
	subDone := make(chan struct{})
	var once sync.Once

	// Handler runs on a managed goroutine — never on the caller's goroutine.
	go func() {
		for {
			select {
			case msg, ok := <-ch:
				if !ok {
					return
				}
				var m backplane.Message
				if err := json.Unmarshal([]byte(msg.Payload), &m); err != nil {
					b.logger.Warn("redis backplane: failed to unmarshal message",
						slog.String("error", err.Error()))
					continue
				}
				// Skip self-messages.
				if m.SourceID == b.nodeID {
					continue
				}
				handler(m)
			case <-subDone:
				return
			case <-b.done:
				return
			}
		}
	}()

	cancel = func() {
		once.Do(func() {
			close(subDone)
			_ = pubsub.Close()
		})
	}
	return cancel, nil
}

func (b *Backplane) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil
	}
	b.closed = true
	close(b.done)
	return nil
}

var _ backplane.Backplane = (*Backplane)(nil)
