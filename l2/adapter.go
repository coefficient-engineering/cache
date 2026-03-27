// Package l2 provides the adapter interface for the core
// cache package.
package l2

import (
	"context"
	"time"
)

// Adapter is the contract for any distributed (L2) cache backend.
//
// cache calls these methods with already-serialized []byte payloads wrapped
// in an envelope that carries logical/physical expiry metadata. The adapter
// does not need to understand Go types, it stores and retrieves opaque bytes.
//
// All implementations must be safe for concurrent use.
type Adapter interface {
	// Get retrieves the raw bytes stored under key.
	// A cache miss MUST return (nil, nil).
	// Only genuine I/O failures should return a non-nil error.
	Get(ctx context.Context, key string) ([]byte, error)

	// Set stores raw bytes under key with the given TTL.
	// A TTL of 0 means the entry does not expire (store indefinitely).
	// Tags associates string labels with this key for bulk invalidation via
	// DeleteByTag. The adapter is responsible for maintaining tag-to-key
	// associations. A nil or empty tags slice means no tag associations.
	Set(ctx context.Context, key string, value []byte, ttl time.Duration, tags []string) error

	// Delete removes key. Must NOT return an error if the key does not exist.
	Delete(ctx context.Context, key string) error

	// DeleteByTag removes all keys that were stored with the given tag.
	// How tag-to-key associations are tracked is entirely up to the implementation.
	DeleteByTag(ctx context.Context, tag string) error

	// Clear removes all entries managed by this adapter.
	// Implementations SHOULD scope this to a configured key prefix as a safety guard
	// against accidentally wiping a shared cache namespace.
	Clear(ctx context.Context) error

	// Ping checks that the backend is reachable.
	// Used by health checks and auto-recovery logic.
	Ping(ctx context.Context) error
}
