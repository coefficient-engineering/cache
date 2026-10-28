package cache

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestFailSafe_ReturnsStaleOnError(t *testing.T) {
	c, clk := newTestCache(t, WithDefaultEntryOptions(EntryOptions{
		Duration:                 1 * time.Minute,
		IsFailSafeEnabled:        true,
		FailSafeMaxDuration:      1 * time.Hour,
		FailSafeThrottleDuration: 30 * time.Second,
	}))
	ctx := context.Background()

	// Initial population
	_, err := c.GetOrSet(ctx, "key", func(_ context.Context, _ *FactoryExecutionContext) (any, error) {
		return "fresh", nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Advance past logical expiry but before physical expiry
	clk.Advance(2 * time.Minute)

	// Factory now fails
	val, err := c.GetOrSet(ctx, "key", failingFactory(errors.New("db down")))
	if err != nil {
		t.Fatalf("expected fail-safe to handle error, got: %v", err)
	}
	if val != "fresh" {
		t.Fatalf("expected stale value 'fresh', got %v", val)
	}
}

func TestFailSafe_ThrottlePreventsRepeatedFactoryCalls(t *testing.T) {
	c, clk := newTestCache(t, WithDefaultEntryOptions(EntryOptions{
		Duration:                 1 * time.Minute,
		IsFailSafeEnabled:        true,
		FailSafeMaxDuration:      1 * time.Hour,
		FailSafeThrottleDuration: 30 * time.Second,
	}))
	ctx := context.Background()

	// Initial population
	_, err := c.GetOrSet(ctx, "key", func(_ context.Context, _ *FactoryExecutionContext) (any, error) {
		return "fresh", nil
	})
	if err != nil {
		t.Fatal(err)
	}

	clk.Advance(2 * time.Minute)

	// First fail-safe activation
	var factoryCalls atomic.Int32
	failFactory := func(_ context.Context, _ *FactoryExecutionContext) (any, error) {
		factoryCalls.Add(1)
		return nil, errors.New("still down")
	}
	if _, err := c.GetOrSet(ctx, "key", failFactory); err != nil {
		t.Fatal(err)
	}

	// Second call within throttle window, should not call factory
	if _, err := c.GetOrSet(ctx, "key", failFactory); err != nil {
		t.Fatal(err)
	}

	if factoryCalls.Load() != 1 {
		t.Fatalf("factory called %d times during throttle, want 1", factoryCalls.Load())
	}
}

func TestFailSafe_Disabled_PropagatesError(t *testing.T) {
	c, clk := newTestCache(t, WithDefaultEntryOptions(EntryOptions{
		Duration:          1 * time.Minute,
		IsFailSafeEnabled: false, // fail-safe OFF
	}))
	ctx := context.Background()

	_, err := c.GetOrSet(ctx, "key", func(_ context.Context, _ *FactoryExecutionContext) (any, error) {
		return "fresh", nil
	})
	if err != nil {
		t.Fatal(err)
	}

	clk.Advance(2 * time.Minute)

	dbErr := errors.New("db down")
	_, err = c.GetOrSet(ctx, "key", failingFactory(dbErr))
	if !errors.Is(err, dbErr) {
		t.Fatalf("expected error to propagate, got: %v", err)
	}
}
