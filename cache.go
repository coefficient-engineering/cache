package cache

import (
	"context"
	"fmt"
)

type Cache interface {
	// Get returns the cached value for key if present and not logically expired.
	// Returns (nil, false, nil) on a clean cache miss.
	Get(ctx context.Context, key string, opts ...EntryOption) (any, bool, error)

	// Set stores value under key in all configured cache layers.
	Set(ctx context.Context, key string, value any, opts ...EntryOption) error

	// GetOrSet returns the cached value for key. On a cache miss it calls factory,
	// stores the result, and returns it. This is the primary method.
	GetOrSet(ctx context.Context, key string, factory FactoryFunc, opts ...EntryOption) (any, error)

	// Delete removes the entry from all cache layers and notifies the backplane.
	Delete(ctx context.Context, key string, opts ...EntryOption) error

	// DeleteByTag removes all entreis associated with tag from all layers.
	DeleteByTag(ctx context.Context, tag string, opts ...EntryOption) error

	// Expire marks the entry as logically expired without removing it.
	// The value remains available as a fail-safe fallback until physical expiry.
	// If fail-safe is not enabled, this is equivalent to Delete.
	Expire(ctx context.Context, key string, opts ...EntryOption) error

	// Clear removes all entries from L1. If clearL2 is true and an L2 adapter
	// is configured, it calls Clear on the adapter as well.
	Clear(ctx context.Context, clearL2 bool) error

	// Name returns the configured cache name.
	Name() string

	// DefaultEntryOptions returns a copy of the cache-wide default entry options.
	DefaultEntryOptions() EntryOptions

	// Events returns the EventEmitter for subscribing to cache lifecycle events.
	Events() *EventEmitter

	// Close shuts down background goroutines and releases resources.
	// It is safe to call Close multiple times.
	Close() error
}

// FactoryFunc is the function called on a cache miss to produce a fresh value.
// It recieves the request context and should respect cancellation.
type FactoryFunc func(ctx context.Context) (any, error)

func GetOrSet[T any](
	ctx context.Context,
	c Cache,
	key string,
	factory func(ctx context.Context) (T, error),
	opts ...EntryOption,
) (T, error) {
	var zero T
	raw, err := c.GetOrSet(ctx, key, func(ctx context.Context) (any, error) {
		return factory(ctx)
	}, opts...)
	if err != nil {
		return zero, err
	}
	if raw == nil {
		return zero, nil
	}
	v, ok := raw.(T)
	if !ok {
		return zero, fmt.Errorf("cache: type mismatch: want %T, got %T", zero, raw)
	}
	return v, nil
}

func Get[T any](ctx context.Context, c Cache, key string, opts ...EntryOption) (T, bool, error) {
	var zero T
	raw, ok, err := c.Get(ctx, key, opts...)
	if err != nil || !ok {
		return zero, ok, err
	}
	v, ok2 := raw.(T)
	if !ok2 {
		return zero, false, fmt.Errorf("cache: type mismatch: want %T, got %T", zero, raw)
	}
	return v, true, nil
}
