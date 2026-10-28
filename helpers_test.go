package cache

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coefficient-engineering/cache/internal/clock"
)

// newTestCache creates a cache with a mock clock for deterministic testing.
func newTestCache(t *testing.T, opts ...Option) (Cache, *clock.Mock) {
	t.Helper()
	clk := clock.NewMock(time.Now())
	c, err := New(opts...)
	if err != nil {
		t.Fatalf("failed to create cache: %v", err)
	}
	// inject mock clock
	setClock(c, clk)
	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Errorf("failed to close cache: %v", err)
		}
	})
	return c, clk
}

// factoryCounter returns a factory that counts calls and returns a fixed value.
func factoryCounter(value any) (FactoryFunc, *atomic.Int32) {
	count := &atomic.Int32{}
	factory := func(_ context.Context, _ *FactoryExecutionContext) (any, error) {
		count.Add(1)
		return value, nil
	}
	return factory, count
}

// failingFactory returns a factory that always returns the given error.
func failingFactory(err error) FactoryFunc {
	return func(_ context.Context, _ *FactoryExecutionContext) (any, error) {
		return nil, err
	}
}

// slowFactory returns a factory that sleeps for d before returning value.
func slowFactory(d time.Duration, value any) FactoryFunc {
	return func(ctx context.Context, _ *FactoryExecutionContext) (any, error) {
		select {
		case <-time.After(d):
			return value, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

func setClock(c Cache, clk clock.Clock) {
	c.(*cache).clock = clk
}
