package cache

import (
	"crypto/rand"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/coefficient-engineering/cache/backplane"
	"github.com/coefficient-engineering/cache/internal/clock"
	"github.com/coefficient-engineering/cache/l2"
	"github.com/coefficient-engineering/cache/serializer"
)

func newCache(opts *Options) (*cache, error) {
	c := &cache{
		opts:       *opts,
		logger:     opts.Logger,
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

// Option configures an Options at construction time.
type Option func(*Options)

func WithCacheName(name string) Option {
	return func(o *Options) { o.CacheName = name }
}

func WithKeyPrefix(prefix string) Option {
	return func(o *Options) { o.KeyPrefix = prefix }
}

func WithL2(adapter l2.Adapter) Option {
	return func(o *Options) { o.L2 = adapter }
}

func WithSerializer(s serializer.Serializer) Option {
	return func(o *Options) { o.Serializer = s }
}

func WithBackplane(bp backplane.Backplane) Option {
	return func(o *Options) { o.Backplane = bp }
}

func WithLogger(logger *slog.Logger) Option {
	return func(o *Options) { o.Logger = logger }
}

func WithDefaultEntryOptions(eo EntryOptions) Option {
	return func(o *Options) { o.DefaultEntryOptions = eo }
}

func WithL2CircuitBreaker(threshold int, openDuration time.Duration) Option {
	return func(o *Options) {
		o.DistributedCacheCircuitBreakerThreshold = threshold
		o.DistributedCacheCircuitBreakerDuration = openDuration
	}
}

func WithBackplaneCircuitBreaker(threshold int, openDuration time.Duration) Option {
	return func(o *Options) {
		o.BackplaneCircuitBreakerThreshold = threshold
		o.BackplaneCircuitBreakerDuration = openDuration
	}
}

func WithNodeID(id string) Option {
	return func(o *Options) { o.NodeID = id }
}

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

	// Validation
	if o.L2 != nil && o.Serializer == nil {
		return nil, fmt.Errorf("cache: an L2 adapter requires a Serializer — add WithSerializer(...) to New()")
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
	_, _ = rand.Read(b)
	return fmt.Sprintf("%x", b)
}
