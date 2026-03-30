// Package backplane defines the [Backplane] interface for inter-node
// cache invalidation.
//
// When a cache instance writes, deletes, or expires an entry, it publishes
// a [Message] to the backplane. Other nodes receive the message and evict
// or expire the corresponding entry from their local L1 cache, keeping all
// nodes consistent without polling the distributed L2 layer.
//
// # How the cache processes incoming messages
//
// When the cache receives a message from the backplane:
//
//  1. Messages with a different CacheName are ignored.
//  2. Messages whose SourceID matches the local NodeID are ignored.
//  3. If IgnoreIncomingBackplaneNotifications is set, all messages are ignored.
//  4. Otherwise:
//     - [MessageTypeSet] and [MessageTypeDelete]: the key is evicted from L1
//     and removed from the tag index.
//     - [MessageTypeExpire]: the L1 entry's logical expiry is set to now;
//     the value is retained for fail-safe.
//     - [MessageTypeClear]: L1 is cleared and the tag index is reset.
//
// # Choosing a transport
//
// The implementation decides the transport. Common choices include
// Redis pub/sub, NATS, Kafka, gRPC broadcast, or an in-process Go
// channel for tests. A no-op adapter is available for single-node
// deployments.
//
// # Concurrency
//
// All [Backplane] implementations must be safe for concurrent use from
// multiple goroutines.
package backplane

import "context"

// MessageType identifies the kind of cache mutation being broadcast.
type MessageType uint8

const (
	// MessageTypeSet indicates a key was written.
	MessageTypeSet MessageType = 1

	// MessageTypeDelete indicates a key was deleted.
	MessageTypeDelete MessageType = 2

	// MessageTypeExpire indicates a key was logically expired.
	// The value is retained in L1 as a fail-safe fallback.
	MessageTypeExpire MessageType = 3

	// MessageTypeClear indicates the entire cache was cleared.
	// [Message.Key] is empty for this type.
	MessageTypeClear MessageType = 4
)

// Message is a single invalidation notification sent between nodes.
//
// The cache populates CacheName from the configured cache name and Key
// from the unprefixed cache key. SourceID is set by the [Backplane]
// implementation (typically to the local node ID) before publishing.
type Message struct {
	// Type is the kind of mutation.
	Type MessageType

	// CacheName identifies the cache instance. This allows multiple
	// named cache instances to share one backplane channel.
	CacheName string

	// Key is the affected key. Empty for [MessageTypeClear].
	Key string

	// SourceID is the node ID of the sender. Receivers use this to
	// skip self-messages.
	SourceID string
}

// Handler is called for each inbound message arriving from another node.
// The [Backplane] implementation must call Handler from a goroutine it
// manages — never on the caller's goroutine.
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
// # Three rules for implementers
//
//  1. [Backplane.Publish] must set [Message.SourceID] before publishing
//     if it has not been set by the caller.
//  2. [Backplane.Subscribe] must call the [Handler] from a goroutine the
//     implementation manages, never on the caller's goroutine.
//  3. All methods must be safe for concurrent use.
type Backplane interface {
	// Publish sends msg to all other nodes subscribed to this backplane.
	// The implementation MUST set [Message.SourceID] before publishing
	// if it has not been set.
	Publish(ctx context.Context, msg Message) error

	// Subscribe registers handler to receive inbound messages.
	// The implementation MUST call handler from a goroutine it manages,
	// never on the caller's goroutine.
	// The returned cancel func stops the subscription and frees resources.
	Subscribe(handler Handler) (cancel func(), err error)

	// Close shuts down the transport connection and releases resources.
	Close() error
}
