package cache

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestSoftTimeout_ReturnsStaleWhileFactoryContinues(t *testing.T) {
	c, clk := newTestCache(t, WithDefaultEntryOptions(EntryOptions{
		Duration:                                 1 * time.Minute,
		IsFailSafeEnabled:                        true,
		FailSafeMaxDuration:                      1 * time.Hour,
		FailSafeThrottleDuration:                 30 * time.Second,
		FactorySoftTimeout:                       50 * time.Millisecond,
		FactoryHardTimeout:                       2 * time.Second,
		AllowTimedOutFactoryBackgroundCompletion: true,
	}))
	ctx := context.Background()

	// Initial value
	c.GetOrSet(ctx, "key", func(ctx context.Context) (any, error) {
		return "v1", nil
	})

	clk.Advance(2 * time.Minute)

	// Slow factory, will exceed soft timeout
	val, err := c.GetOrSet(ctx, "key", slowFactory(500*time.Millisecond, "v2"))
	if err != nil {
		t.Fatal(err)
	}
	// Should get stale value immediately (before factory completes)
	if val != "v1" {
		t.Fatalf("expected stale value v1, got %v", val)
	}

	// Wait for background factory to complete
	time.Sleep(600 * time.Millisecond)

	// Now the fresh value should be cached
	val, ok, _ := c.Get(ctx, "key")
	if ok && val == "v2" {
		// Background completion stored the new value
	}
}

func TestHardTimeout_NoStale_ReturnsError(t *testing.T) {
	c, _ := newTestCache(t, WithDefaultEntryOptions(EntryOptions{
		Duration:           1 * time.Minute,
		FactoryHardTimeout: 50 * time.Millisecond,
	}))
	ctx := context.Background()

	_, err := c.GetOrSet(ctx, "key", slowFactory(1*time.Second, "never"))
	if !errors.Is(err, ErrFactoryHardTimeout) {
		t.Fatalf("expected ErrFactoryHardTimeout, got: %v", err)
	}
}
