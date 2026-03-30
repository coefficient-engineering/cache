package cache

// FactoryExecutionContext provides adaptive caching capabilities
// to factory functions. It is passed to every FactoryFunc invocation
// and allows the factory to modify cache entry options based on the
// value being cached.
//
// Mutate the Options pointer to change how the entry is stored.
// For example, set Options.Duration to control TTL based on the
// fetched value's characteristics.
type FactoryExecutionContext struct {
	// Options is a mutable pointer to the EntryOptions for this cache
	// operation. Changes made by the factory are honoured when the
	// value is stored.
	Options *EntryOptions

	// StaleValue is the previously cached (now stale) value, if one
	// exists. Nil when there is no stale entry.
	StaleValue any

	// HasStaleValue is true when a stale entry was found in L1 or L2.
	// Use this to distinguish a nil stale value from "no stale entry".
	HasStaleValue bool
}
