package l1

// Adapter is the contract for the in-process (L1) memory cache.
//
// cache uses this to store and retrieve *cacheEntry values keyed by
// prefixed string keys. You can swap in bounded caches like Ristretto, Theine,
// or Otter by implementing this interface.
//
// All implementations must be safe for concurrent use.
type Adapter interface {
	// Get retrieves the entry stored under key.
	// Returns (nil, false) on a cache miss — never an error.
	Get(key string) (any, bool)

	// Set stores value under key, unconditionally overwriting any
	// existing entry.
	//
	// cost is an advisory hint representing the entry's relative weight
	// (e.g., serialized byte size). Bounded caches use this to make
	// eviction decisions. Unbounded implementations (like sync.Map) may
	// ignore it.
	Set(key string, value any, cost int64)

	// Delete removes the entry for key. Must not panic if the key
	// does not exist.
	Delete(key string)

	// LoadAndDelete atomically retrieves and removes the entry for key.
	// If the key was present, returns (value, true). Otherwise (nil, false).
	LoadAndDelete(key string) (any, bool)

	// Range calls fn sequentially for every entry in the cache.
	// If fn returns false, iteration stops.
	//
	// The iteration order is not guaranteed.
	// fn must not call other methods on the Adapter, doing so may deadlock.
	Range(fn func(key string, value any) bool)

	// CompareAndSwap atomically replaces the value for key only if the
	// current value is identical (==) to old. Returns true if the swap
	// was performed. This is used by Expire to update an entry in-place
	// without racing with a concurrent Set from another goroutine.
	CompareAndSwap(key string, old, new any) bool

	// Clear removes all entries from the cache.
	Clear()

	// Close releases any resources held by the adapter (background
	// goroutines, metric buffers, etc.). Implementations that have
	// nothing to clean up should return nil.
	Close() error
}
