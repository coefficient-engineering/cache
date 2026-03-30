package cache

import (
	"crypto/rand"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/coefficient-engineering/cache/adapters/l1/syncmap"
	"github.com/coefficient-engineering/cache/backplane"
	"github.com/coefficient-engineering/cache/internal/clock"
	"github.com/coefficient-engineering/cache/l1"
	"github.com/coefficient-engineering/cache/l2"
	"github.com/coefficient-engineering/cache/serializer"
)

func newCache(opts *Options) (*cache, error) {
	l1Adapter := opts.L1
	if l1Adapter == nil {
		l1Adapter = syncmap.New() // default to sync.Map
	}
	c := &cache{
		opts:       *opts,
		logger:     opts.Logger,
		l1:         l1Adapter,
		l2:         opts.L2,
		serializer: opts.Serializer,
		bp:         opts.Backplane,
		clock:      clock.Real{},
		done:       make(chan struct{}),
	}

	c.l2CB = newCircuitBreaker(
		opts.DistributedCacheCircuitBreakerThreshold,
		opts.DistributedCacheCircuitBreakerDuration,
	)
	c.bpCB = newCircuitBreaker(
		opts.BackplaneCircuitBreakerThreshold,
		opts.BackplaneCircuitBreakerDuration,
	)
	c.bpCB.onRecovery = c.attemptAutoRecovery
	// listen for bp invalidation messages
	if c.bp != nil {
		cancel, err := c.bp.Subscribe(c.handleBackplaneMessage)
		if err != nil {
			return nil, fmt.Errorf("cache: backplane subscribe: %w", err)
		}
		c.bpCancelSub = cancel
	}

	return c, nil
}

// Option configures an [Options] value at construction time.
// Pass Option values to [New].
type Option func(*Options)

// WithCacheName sets [Options.CacheName], which identifies this cache
// instance in logs, events, and backplane messages. Default: "default".
func WithCacheName(name string) Option {
	return func(o *Options) { o.CacheName = name }
}

// WithKeyPrefix sets [Options.KeyPrefix], prepended to every key before
// L1, L2, and backplane access. Enables namespace isolation when multiple
// caches share an L2 backend.
func WithKeyPrefix(prefix string) Option {
	return func(o *Options) { o.KeyPrefix = prefix }
}

// WithL1 sets [Options.L1], the in-process memory cache adapter.
// If not set, a default [github.com/coefficient-engineering/cache/adapters/l1/syncmap]
// adapter is used.
func WithL1(adapter l1.Adapter) Option {
	return func(o *Options) { o.L1 = adapter }
}

// WithL2 sets [Options.L2], the distributed cache adapter.
// Requires [WithSerializer] to also be set.
func WithL2(adapter l2.Adapter) Option {
	return func(o *Options) { o.L2 = adapter }
}

// WithSerializer sets [Options.Serializer], which encodes and decodes Go
// values for L2 storage and auto-clone. Required when [WithL2] is used.
func WithSerializer(s serializer.Serializer) Option {
	return func(o *Options) { o.Serializer = s }
}

// WithBackplane sets [Options.Backplane], the inter-node invalidation
// transport. If nil, no cross-node notifications are sent or received.
func WithBackplane(bp backplane.Backplane) Option {
	return func(o *Options) { o.Backplane = bp }
}

// WithLogger sets [Options.Logger], the structured logger for internal
// diagnostics. If not set, all logging is silently discarded.
func WithLogger(logger *slog.Logger) Option {
	return func(o *Options) { o.Logger = logger }
}

// WithDefaultEntryOptions sets [Options.DefaultEntryOptions], the baseline
// [EntryOptions] for every cache operation. Per-call [EntryOption] functions
// are applied on top of a copy of this value.
func WithDefaultEntryOptions(eo EntryOptions) Option {
	return func(o *Options) { o.DefaultEntryOptions = eo }
}

// WithL2CircuitBreaker configures the L2 circuit breaker. threshold is the
// number of consecutive L2 errors before the breaker opens. openDuration
// is how long the breaker stays open before attempting recovery. A threshold
// of 0 disables the L2 circuit breaker.
func WithL2CircuitBreaker(threshold int, openDuration time.Duration) Option {
	return func(o *Options) {
		o.DistributedCacheCircuitBreakerThreshold = threshold
		o.DistributedCacheCircuitBreakerDuration = openDuration
	}
}

// WithBackplaneCircuitBreaker configures the backplane circuit breaker.
// threshold is the consecutive-failure count before the breaker opens.
// openDuration is how long it stays open. A threshold of 0 disables it.
func WithBackplaneCircuitBreaker(threshold int, openDuration time.Duration) Option {
	return func(o *Options) {
		o.BackplaneCircuitBreakerThreshold = threshold
		o.BackplaneCircuitBreakerDuration = openDuration
	}
}

// WithNodeID sets [Options.NodeID], which uniquely identifies this cache
// node in backplane messages for self-message suppression. If not set, a
// random 16-byte hex string is generated.
func WithNodeID(id string) Option {
	return func(o *Options) { o.NodeID = id }
}

// New creates and returns a new [Cache] instance.
//
// Validation:
//   - If L2 is non-nil and Serializer is nil, returns an error.
//
// Side effects at construction:
//   - If NodeID is empty, generates a random 16-byte hex string.
//   - If Logger is nil, creates a discard logger.
//   - If L1 is nil, creates a default syncmap adapter.
//   - If Backplane is non-nil, calls Subscribe to start receiving messages.
//   - Creates circuit breakers for L2 and backplane (disabled when threshold is 0).
func New(opts ...Option) (Cache, error) {
	o := &Options{
		CacheName:              "default",
		BackplaneAutoRecovery:  true,
		AutoRecoveryMaxRetries: 10,
		AutoRecoveryDelay:      2 * time.Second,
		SkipL2OnError:          true,
		DefaultEntryOptions: EntryOptions{
			Duration:                                 5 * time.Minute,
			AllowTimedOutFactoryBackgroundCompletion: true,
			AllowBackgroundBackplaneOperations:       true,
			ReThrowSerializationExceptions:           true,
			Priority:                                 PriorityNormal,
		},
	}

	for _, opt := range opts {
		opt(o)
	}

	// Validate
	if o.L2 != nil && o.Serializer == nil {
		return nil, fmt.Errorf("cache: an L2 adapter requires a Serializer, add WithSerializer(...) to New()")
	}

	// Generate a random node ID if none provided
	if o.NodeID == "" {
		o.NodeID = generateNodeID()
	}

	// Discard logger if none provided
	if o.Logger == nil {
		o.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}

	return newCache(o)
}

func generateNodeID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic(fmt.Sprintf("failed to generate node ID: %v", err))
	}
	return fmt.Sprintf("%x", b)
}
