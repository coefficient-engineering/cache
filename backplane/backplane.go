// Package backplane defines the [Backplane] interface for inter-node
// cache invalidation.
//
// Cache mutations publish a [Message]. Other nodes evict or expire matching
// local L1 entries when delivery completes, without polling L2. Delivery is
// best-effort: publishing is not a cross-node processing acknowledgement.
//
// # How the cache processes incoming messages
//
// When the cache receives a message from the backplane:
//
//  1. Messages with a different CacheName or KeyPrefix are ignored.
//  2. Messages whose SourceID matches the local NodeID are ignored.
//  3. If IgnoreIncomingBackplaneNotifications is set, all messages are ignored.
//  4. Otherwise:
//     - [MessageTypeSet] and [MessageTypeDelete]: the key is evicted from L1
//     and removed from the tag index.
//     - [MessageTypeExpire]: the L1 entry's logical expiry is set to now;
//     the value is retained for fail-safe.
//     - [MessageTypeClear]: L1 is cleared and the tag index is reset.
//     - [MessageTypeDeleteByTags]: entries carrying any listed tag are evicted,
//     including remote-only and stale/fail-safe entries.
//     Unrelated entries stay.
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

	// MessageTypeDeleteByTags evicts entries carrying any of [Message.Tags].
	// Receivers use their own tag index, including remote-only entries.
	MessageTypeDeleteByTags MessageType = 5
)

// Message is a single invalidation notification sent between nodes.
//
// The cache populates CacheName, KeyPrefix, and SourceID from configuration.
// Key is unprefixed for key-level mutations; Tags identifies a union for tag
// invalidation. Transports must preserve all fields.
type Message struct {
	// Type is the kind of mutation.
	Type MessageType

	// CacheName identifies the cache instance. This allows multiple
	// named cache instances to share one backplane channel.
	CacheName string

	// KeyPrefix identifies the key namespace within CacheName.
	// Receivers require an exact match, including the empty prefix.
	KeyPrefix string

	// Key is the unprefixed affected key. Empty for Clear and DeleteByTags.
	Key string

	// Tags is the union of tags to invalidate for MessageTypeDeleteByTags.
	// Other message types leave it empty. Publishers must not mutate this
	// slice after publishing; transports must preserve it.
	Tags []string

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
