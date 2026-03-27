// Package backplane provides the interfaces for implementing a backplane
// in the core cache package.
package backplane

import "context"

// MessageType identifies this kind of cache mutation being broadcast.
type MessageType uint8

const (
	MessageTypeSet    MessageType = 1 // A key was written
	MessageTypeDelete MessageType = 2 // A key was deleted
	MessageTypeExpire MessageType = 3 // A key was logically expired
	MessageTypeClear  MessageType = 4 // The cache was cleared
)

// Message is a single invalidation notification sent between nodes.
type Message struct {
	Type      MessageType
	CacheName string // allows multiple named cache instances on one backplane channel
	Key       string // empty for MessageTypeClear
	SourceID  string // node ID of the sender. receivers will use this to skip self-messages
}

// Handler is called for each inbound message arriving from another node.
type Handler func(msg Message)

// Backplane is the contract for inter-node cache invalidation.
//
// The implementation decides the transport. Common choices:
//   - Redis pub/sub
//   - NATS Core or JetStream
//   - Kafka
//   - gRPC broadcast
//   - In-process Go channel (for tests)
//   - No-op (single-node deployments)
//
// Implementations must be safe for concurrent use.
type Backplane interface {
	// Publish sends msg to all other nodes subscribed to this backplane.
	// The implementation MUST set msg.SourceID before publishing if it has not been set.
	Publish(ctx context.Context, msg Message) error

	// Subscribe registers handler to receive inbound messages.
	// The implementation MUST call handler from a goroutine it manages, never on the caller's goroutine.
	// The returned cancel func stops the subscription and frees resources.
	Subscribe(handler Handler) (cancel func(), err error)

	// Close shuts down the transport connection.
	Close() error
}
