package cache

import (
	"math/rand"
	"sync/atomic"
	"time"
)

// cacheEntry is the value stored in the L1 memory cache.
// Once created it is treated as immutable; mutations produce a new entry.
type cacheEntry struct {
	// The cached value as returned by the factory or deserialized from L2.
	value any

	// logicalExpiry is when this value is considered stale from the caller's
	// perspective. Corresponds to the entry's Duration option.
	logicalExpiry time.Time

	// physicalExpiry is the actual wall-clock removal time.
	//   - fail-safe OFF: physicalExpiry == logicalExpiry
	//   - fail-safe ON:  physicalExpiry == createdAt + FailSafeMaxDuration
	physicalExpiry time.Time

	// createdAt is needed to compute eager refresh eligibility.
	createdAt time.Time

	// eagerRefreshRunning guards against duplicate background refresh goroutines.
	// CAS from 0 → 1 before starting; reset to 0 on completion.
	eagerRefreshRunning atomic.Int32

	// tags are the tag labels associated with this entry.
	tags []string
}

func (e *cacheEntry) IsLogicallyExpired(now time.Time) bool {
	return now.After(e.logicalExpiry)
}

func (e *cacheEntry) IsPhysicallyExpired(now time.Time) bool {
	return now.After(e.physicalExpiry)
}

// l2Envelope is the wire format for L2 entries. It is JSON-encoded by
// the core library (not the user-facing [serializer.Serializer]) and then
// passed to [l2.Adapter] as opaque []byte. The adapter stores it verbatim.
type l2Envelope struct {
	// V is the inner user value, serialized by the Serializer.
	V []byte `json:"v"`

	// LE is the logical expiry as Unix milliseconds.
	LE int64 `json:"le"`

	// PE is the physical expiry as Unix milliseconds.
	PE int64 `json:"pe"`

	// Tags associated with this entry.
	Tags []string `json:"t,omitempty"`
}

// effectiveTTL returns the duration to pass to L2Adapter.Set.
// It accounts for fail-safe (use physical TTL) and jitter.
func effectiveTTL(opts EntryOptions) time.Duration {
	base := opts.Duration

	// when failsafe is on the physical store duration is the max duration
	if opts.IsFailSafeEnabled && opts.FailSafeMaxDuration > 0 {
		base = opts.FailSafeMaxDuration
	}

	// L2 specific override
	if opts.DistributedCacheDuration > 0 {
		base = opts.DistributedCacheDuration
		if opts.IsFailSafeEnabled && opts.DistributedCacheFailSafeMaxDuration > 0 {
			base = opts.DistributedCacheFailSafeMaxDuration
		}
	}

	// jitter, random additional duration in [0, JitterMaxDuration]
	if opts.JitterMaxDuration > 0 {
		base += time.Duration(rand.Int63n(int64(opts.JitterMaxDuration)))
	}

	return base
}
