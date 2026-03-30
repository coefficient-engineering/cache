package cache

import (
	"log/slog"
	"time"

	"github.com/coefficient-engineering/cache/backplane"
	"github.com/coefficient-engineering/cache/l1"
	"github.com/coefficient-engineering/cache/l2"
	"github.com/coefficient-engineering/cache/serializer"
)

// Options configures the entire cache instance. Set once via [Option]
// functions passed to [New]. Never mutated after construction.
//
// See the individual field documentation for defaults.
// See [WithCacheName], [WithKeyPrefix], [WithL1], [WithL2],
// [WithSerializer], [WithBackplane], [WithLogger], and
// [WithDefaultEntryOptions] for the functional option constructors.
type Options struct {
	// CacheName identifies this cache instance in logs, events, and backplane messages.
	// Default: "default"
	CacheName string

	// KeyPrefix is prepended to every key before it is passed to L1, L2, and the
	// backplane. Enables namespace isolation when multiple caches share an L2 backend.
	// Example: "myapp:products:"
	KeyPrefix string

	// DefaultEntryOptions is the baseline EntryOptions for every cache operation.
	// Per-call EntryOption funcs are applied on top of a copy of this value.
	DefaultEntryOptions EntryOptions

	// L1 is the in-process memory cache adapter.
	// If nil, a default sync.Map-backed adapter is used.
	L1 l1.Adapter

	// L2 is the distributed cache adapter.
	// If nil, cache operates as a pure in-process memory cache.
	L2 l2.Adapter

	// Serializer is required when L2 is non-nil.
	// It encodes/decodes Go values to/from []byte for L2 storage.
	Serializer serializer.Serializer

	// Backplane enables inter-node invalidation.
	// If nil, no cross-node notifications are sent or received.
	Backplane backplane.Backplane

	// Logger is the structured logger for internal diagnostics.
	// If nil, all logging is silently discarded.
	Logger *slog.Logger

	// NodeID uniquely identifies this cache node in backplane messages.
	// Used to suppress processing of self-sent notifications.
	// If empty, a random UUID is generated at construction time.
	NodeID string

	// DistributedCacheCircuitBreakerThreshold is the number of consecutive L2
	// errors that cause the circuit breaker to open (reject further L2 calls).
	// 0 disables the L2 circuit breaker entirely.
	DistributedCacheCircuitBreakerThreshold int

	// DistributedCacheCircuitBreakerDuration is how long the L2 circuit
	// breaker stays open before attempting recovery.
	DistributedCacheCircuitBreakerDuration time.Duration

	// BackplaneCircuitBreakerThreshold is the consecutive-failure threshold
	// for the backplane circuit breaker. 0 disables it.
	BackplaneCircuitBreakerThreshold int

	// BackplaneCircuitBreakerDuration is how long the backplane circuit
	// breaker stays open. Only relevant when threshold > 0.
	BackplaneCircuitBreakerDuration time.Duration

	// BackplaneAutoRecovery enables automatic L1 re-sync when the backplane
	// reconnects after an outage. Default: true.
	BackplaneAutoRecovery bool

	// AutoRecoveryMaxRetries is the maximum number of recovery attempts.
	// Default: 10.
	AutoRecoveryMaxRetries int

	// AutoRecoveryDelay is the pause between recovery attempts.
	// Default: 2s.
	AutoRecoveryDelay time.Duration

	// IgnoreIncomingBackplaneNotifications disables acting on received backplane
	// messages. Useful for read-only replicas or during testing.
	IgnoreIncomingBackplaneNotifications bool

	// SkipL2OnError: if true (default), L2 errors are logged and swallowed.
	// cache continues with L1 only. If false, L2 errors propagate to callers
	// unless overridden by the per-entry ReThrowDistributedCacheExceptions flag.
	SkipL2OnError bool
}

// EntryOptions holds all per-entry settings. It is a plain value type;
// the cache copies it on every call so mutations never affect the stored
// defaults.
//
// Per-call configuration is expressed as [EntryOption] functions.
// The cache copies [Options.DefaultEntryOptions] and applies the provided
// funcs before each operation via [applyOptions].
//
// # Defaults
//
// When [Options.DefaultEntryOptions] is not explicitly set, [New] uses:
//
//   - Duration: 5 * time.Minute
//   - AllowTimedOutFactoryBackgroundCompletion: true
//   - AllowBackgroundBackplaneOperations: true
//   - ReThrowSerializationExceptions: true
//   - Priority: [PriorityNormal]
//
// All other fields default to their zero value.
type EntryOptions struct {
	// Duration is how long the entry is considered fresh (logically valid).
	// When fail-safe is enabled the physical TTL in backing stores is
	// FailSafeMaxDuration; Duration marks the "stale after" boundary.
	Duration time.Duration

	// DistributedCacheDuration overrides Duration for the L2 layer only.
	// Zero means "use Duration".
	DistributedCacheDuration time.Duration

	// JitterMaxDuration adds a random extra TTL in [0, JitterMaxDuration) to
	// both L1 and L2 entries. Prevents thundering-herd expiry spikes across
	// nodes in multi-instance deployments. Zero disables jitter.
	JitterMaxDuration time.Duration

	// IsFailSafeEnabled activates the fail-safe mechanism: if a factory call
	// or L2 fetch fails and a stale entry exists, the stale value is returned
	// rather than propagating the error.
	IsFailSafeEnabled bool

	// FailSafeMaxDuration is the total lifetime of an entry in the backing
	// store when fail-safe is on. The entry will be physically present (but
	// logically stale) for this duration after it was first written, enabling
	// it to be used as a fallback.
	// Must be >= Duration when fail-safe is enabled.
	FailSafeMaxDuration time.Duration

	// DistributedCacheFailSafeMaxDuration overrides FailSafeMaxDuration for
	// the L2 physical TTL. Zero means "use FailSafeMaxDuration".
	DistributedCacheFailSafeMaxDuration time.Duration

	// FailSafeThrottleDuration is how long a fail-safe-activated stale value
	// is temporarily promoted back to "fresh" in L1 to prevent the factory
	// from being hammered again immediately after an error.
	FailSafeThrottleDuration time.Duration

	// AllowStaleOnReadOnly permits stale (logically expired) values to be
	// returned from read-only operations (Get) without triggering a factory call.
	AllowStaleOnReadOnly bool

	// FactorySoftTimeout is the maximum time to wait for the factory before
	// returning a stale fail-safe value to the caller. The factory continues
	// running in the background and caches its result when done.
	// Zero means no soft timeout.
	FactorySoftTimeout time.Duration

	// FactoryHardTimeout is the absolute maximum time to wait for the factory.
	// After this the call returns an error (or stale value if fail-safe is on),
	// regardless of whether a stale value is available.
	// Zero means wait indefinitely.
	FactoryHardTimeout time.Duration

	// AllowTimedOutFactoryBackgroundCompletion: when true, a factory that
	// triggered a soft or hard timeout (but eventually succeeds) will have its
	// result stored in the cache. Default: true.
	AllowTimedOutFactoryBackgroundCompletion bool

	// DistributedCacheSoftTimeout is the max time to wait for an L2 read/write
	// before falling back to a stale value (fail-safe must be on for a fallback
	// to be available). Zero means no soft timeout.
	DistributedCacheSoftTimeout time.Duration

	// DistributedCacheHardTimeout is the absolute max time for any L2 operation.
	// Zero means wait indefinitely.
	DistributedCacheHardTimeout time.Duration

	// AllowBackgroundDistributedCacheOperations: when true, L2 writes are
	// fire-and-forget goroutines. This improves latency but means a write
	// failure is logged rather than returned to the caller.
	// Default: false (blocking, deterministic behaviour).
	AllowBackgroundDistributedCacheOperations bool

	// EagerRefreshThreshold: when a cache hit occurs after this fraction of
	// Duration has elapsed, a background factory call is started to refresh
	// the entry before it expires, so callers never observe a miss.
	// Value must be in (0.0, 1.0); zero or values outside this range disable
	// eager refresh.
	// Example: 0.9 starts refreshing at 90% of Duration elapsed.
	EagerRefreshThreshold float32

	// SkipBackplaneNotifications: if true, mutations (Set/Delete/Expire) will
	// not publish backplane messages for this operation.
	SkipBackplaneNotifications bool

	// AllowBackgroundBackplaneOperations: when true, backplane publishes are
	// fire-and-forget goroutines. Default: true.
	AllowBackgroundBackplaneOperations bool

	// ReThrowBackplaneExceptions: when true and AllowBackgroundBackplaneOperations
	// is false, backplane publish errors are returned to the caller.
	ReThrowBackplaneExceptions bool

	// ReThrowDistributedCacheExceptions: when true and AllowBackgroundDistributedCacheOperations
	// is false, L2 errors are returned to the caller.
	ReThrowDistributedCacheExceptions bool

	// ReThrowSerializationExceptions: when true, serialization errors during L2
	// reads/writes are returned to the caller. Default: true.
	ReThrowSerializationExceptions bool

	// Priority hints to the L1 eviction policy. Higher priority entries are
	// evicted last under memory pressure. Default: PriorityNormal.
	Priority EvictionPriority

	// Size is an arbitrary weight unit used by L1 when a SizeLimit is configured
	// on the cache. Typically represents relative byte size or item weight.
	Size int64

	// SkipL1Read: bypass reading from the in-process memory cache (L1).
	// Use with care, removes stampede protection.
	SkipL1Read bool

	// SkipL1Write: bypass writing to L1 after a factory call or L2 hit.
	SkipL1Write bool

	// SkipL2Read: bypass reading from the distributed cache (L2).
	SkipL2Read bool

	// SkipL2Write: bypass writing to L2.
	SkipL2Write bool

	// SkipL2ReadWhenStale: when L1 has a stale entry, skip checking L2 for a
	// newer version. Useful when L2 is local (not shared across nodes).
	SkipL2ReadWhenStale bool

	// Tags associates string labels with this entry for bulk invalidation via
	// DeleteByTag. Tags are stored in both L1 (in-memory reverse index) and L2
	// (implementation-defined, e.g., a Redis SET per tag).
	Tags []string

	// LockTimeout is the maximum time to wait to acquire the stampede protection
	// lock for this key. After this, the caller proceeds without the lock
	// (risks a mini-stampede but prevents indefinite starvation).
	// Zero means wait indefinitely.
	LockTimeout time.Duration

	// EnableAutoClone: when true, values returned from L1 are deep-cloned
	// (via the Serializer: marshal -> unmarshal) before being returned to the
	// caller. Prevents callers from inadvertently mutating cached objects.
	// Requires a Serializer to be configured.
	EnableAutoClone bool
}

// EvictionPriority hints to the L1 eviction policy.
// Higher priority entries are evicted last under memory pressure.
// Unbounded L1 adapters (like the default sync.Map adapter) ignore this.
type EvictionPriority int

// Eviction priority constants for the L1 cache.
const (
	PriorityLow         EvictionPriority = -1 // evict first
	PriorityNormal      EvictionPriority = 0  // default
	PriorityHigh        EvictionPriority = 1  // evict last
	PriorityNeverRemove EvictionPriority = 2  // never evict
)

// EntryOption modifies an [EntryOptions] value. Compose multiple options
// freely; they are applied in order on top of a copy of the cache-wide
// [Options.DefaultEntryOptions].
type EntryOption func(*EntryOptions)

// WithDuration sets [EntryOptions.Duration], the logical TTL of the entry.
func WithDuration(d time.Duration) EntryOption {
	return func(o *EntryOptions) { o.Duration = d }
}

// WithFailSafe enables fail-safe and sets [EntryOptions.FailSafeMaxDuration]
// and [EntryOptions.FailSafeThrottleDuration]. When fail-safe is enabled and
// a factory or L2 fetch fails, a stale value (if available) is returned
// instead of an error.
func WithFailSafe(maxDuration, throttleDuration time.Duration) EntryOption {
	return func(o *EntryOptions) {
		o.IsFailSafeEnabled = true
		o.FailSafeMaxDuration = maxDuration
		o.FailSafeThrottleDuration = throttleDuration
	}
}

// WithFactoryTimeouts sets [EntryOptions.FactorySoftTimeout] and
// [EntryOptions.FactoryHardTimeout]. The soft timeout returns a stale value
// early (factory continues in the background); the hard timeout is an
// absolute deadline.
func WithFactoryTimeouts(soft, hard time.Duration) EntryOption {
	return func(o *EntryOptions) {
		o.FactorySoftTimeout = soft
		o.FactoryHardTimeout = hard
	}
}

// WithDistributedCacheTimeouts sets [EntryOptions.DistributedCacheSoftTimeout]
// and [EntryOptions.DistributedCacheHardTimeout] for L2 operations.
func WithDistributedCacheTimeouts(soft, hard time.Duration) EntryOption {
	return func(o *EntryOptions) {
		o.DistributedCacheSoftTimeout = soft
		o.DistributedCacheHardTimeout = hard
	}
}

// WithEagerRefresh sets [EntryOptions.EagerRefreshThreshold]. The threshold
// must be in (0.0, 1.0). For example, 0.9 starts a background refresh when
// 90% of Duration has elapsed.
func WithEagerRefresh(threshold float32) EntryOption {
	return func(o *EntryOptions) { o.EagerRefreshThreshold = threshold }
}

// WithJitter sets [EntryOptions.JitterMaxDuration], adding a random extra
// TTL in [0, max) to prevent thundering-herd expiry spikes.
func WithJitter(max time.Duration) EntryOption {
	return func(o *EntryOptions) { o.JitterMaxDuration = max }
}

// WithTags appends tag labels to [EntryOptions.Tags] for bulk invalidation
// via [Cache.DeleteByTag].
func WithTags(tags ...string) EntryOption {
	return func(o *EntryOptions) {
		newTags := make([]string, len(o.Tags), len(o.Tags)+len(tags))
		copy(newTags, o.Tags)
		o.Tags = append(newTags, tags...)
	}
}

// WithPriority sets [EntryOptions.Priority], an eviction hint for bounded
// L1 adapters.
func WithPriority(p EvictionPriority) EntryOption {
	return func(o *EntryOptions) { o.Priority = p }
}

// WithBackgroundL2Ops sets [EntryOptions.AllowBackgroundDistributedCacheOperations]
// to true, making L2 writes fire-and-forget goroutines.
func WithBackgroundL2Ops() EntryOption {
	return func(o *EntryOptions) { o.AllowBackgroundDistributedCacheOperations = true }
}

// WithSkipL2 sets [EntryOptions.SkipL2Read], [EntryOptions.SkipL2Write],
// and [EntryOptions.SkipBackplaneNotifications] to true, bypassing L2
// and the backplane entirely for this operation.
func WithSkipL2() EntryOption {
	return func(o *EntryOptions) {
		o.SkipL2Read = true
		o.SkipL2Write = true
		o.SkipBackplaneNotifications = true
	}
}

// WithSkipL2ReadWhenStale sets [EntryOptions.SkipL2ReadWhenStale] to true.
// When L1 has a stale entry, the L2 check is skipped.
func WithSkipL2ReadWhenStale() EntryOption {
	return func(o *EntryOptions) { o.SkipL2ReadWhenStale = true }
}

// WithAllowStaleOnReadOnly sets [EntryOptions.AllowStaleOnReadOnly] to true,
// permitting stale values from read-only operations ([Cache.Get]).
func WithAllowStaleOnReadOnly() EntryOption {
	return func(o *EntryOptions) { o.AllowStaleOnReadOnly = true }
}

// WithAutoClone sets [EntryOptions.EnableAutoClone] to true. Values returned
// from L1 are deep-cloned via the [serializer.Serializer] (marshal then
// unmarshal) before being returned, preventing callers from mutating cached
// objects. Requires a Serializer to be configured.
func WithAutoClone() EntryOption {
	return func(o *EntryOptions) { o.EnableAutoClone = true }
}

// WithLockTimeout sets [EntryOptions.LockTimeout], the maximum time to wait
// for the stampede protection lock. Returns [ErrLockTimeout] on timeout.
func WithLockTimeout(d time.Duration) EntryOption {
	return func(o *EntryOptions) { o.LockTimeout = d }
}

// WithSize sets [EntryOptions.Size], an arbitrary weight hint for bounded
// L1 adapters.
func WithSize(n int64) EntryOption {
	return func(o *EntryOptions) { o.Size = n }
}

// applyOptions returns a new EntryOptions with opts applied on top of defaults.
// This is called internally on every cache operation.
func applyOptions(defaults EntryOptions, opts []EntryOption) EntryOptions {
	result := defaults // value copy, never mutates the stored defaults
	// Deep copy Tags slice to avoid shared backing array
	if len(defaults.Tags) > 0 {
		result.Tags = make([]string, len(defaults.Tags))
		copy(result.Tags, defaults.Tags)
	}
	for _, opt := range opts {
		opt(&result)
	}
	return result
}
