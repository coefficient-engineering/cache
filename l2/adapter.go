// Package l2 defines the [Adapter] interface for the distributed (L2)
// cache layer.
//
// The core cache package serializes user values, wraps them in a
// JSON-encoded envelope carrying logical and physical expiry timestamps,
// and passes the resulting []byte to the adapter. The adapter stores and
// retrieves these bytes without needing to understand Go types.
//
// # Data format
//
// The bytes handed to [Adapter.Set] and returned by [Adapter.Get] are a
// JSON-encoded envelope containing:
//
//   - The inner user value (serialized by the configured [serializer.Serializer])
//   - Logical expiry timestamp (when the value becomes stale)
//   - Physical expiry timestamp (when the value is removed entirely)
//   - Tags associated with the entry
//
// # Concurrency
//
// All [Adapter] implementations must be safe for concurrent use from
// multiple goroutines.
package l2

import (
	"context"
	"time"
)

// Adapter is the contract for any distributed (L2) cache backend.
//
// The cache calls these methods with already-serialized []byte payloads
// wrapped in an envelope that carries logical/physical expiry metadata.
// The adapter does not need to understand Go types — it stores and
// retrieves opaque bytes.
//
// # Key contracts
//
//   - [Adapter.Get] must return (nil, nil) on a cache miss, not an error.
//   - [Adapter.Delete] must not return an error if the key does not exist.
//   - [Adapter.Set] receives a tags slice; the adapter is responsible for
//     maintaining tag-to-key associations for [Adapter.DeleteByTag].
//
// All implementations must be safe for concurrent use.
type Adapter interface {
	// Get retrieves the raw bytes stored under key.
	// A cache miss MUST return (nil, nil).
	// Only genuine I/O failures should return a non-nil error.
	Get(ctx context.Context, key string) ([]byte, error)

	// Set stores raw bytes under key with the given TTL.
	// A TTL of 0 means the entry does not expire (store indefinitely).
	//
	// tags associates string labels with this key for bulk invalidation
	// via [Adapter.DeleteByTag]. The adapter is responsible for maintaining
	// tag-to-key associations (e.g., a Redis SET per tag, an in-memory map,
	// a database secondary index). A nil or empty tags slice means no tag
	// associations.
	Set(ctx context.Context, key string, value []byte, ttl time.Duration, tags []string) error

	// Delete removes key. Must NOT return an error if the key does not exist.
	Delete(ctx context.Context, key string) error

	// DeleteByTag removes all keys that were stored with the given tag.
	// How tag-to-key associations are tracked is entirely up to the
	// implementation. If your backend does not support tagging natively
	// and you do not need DeleteByTag, implement it as a no-op returning nil.
	DeleteByTag(ctx context.Context, tag string) error

	// Clear removes all entries managed by this adapter.
	// Implementations SHOULD scope this to a configured key prefix as a
	// safety guard against accidentally wiping a shared cache namespace.
	Clear(ctx context.Context) error

	// Ping checks that the backend is reachable.
	// Used by health checks and the circuit breaker auto-recovery logic.
	Ping(ctx context.Context) error
}
