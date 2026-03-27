package cache

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestEagerRefresh_TriggersAtThreshold(t *testing.T) {
	c, clk := newTestCache(t, WithDefaultEntryOptions(EntryOptions{
		Duration:              10 * time.Minute,
		EagerRefreshThreshold: 0.9, // refresh at 9 minutes
		FactoryHardTimeout:    1 * time.Second,
	}))
	ctx := context.Background()

	var calls atomic.Int32
	factory := func(ctx context.Context) (any, error) {
		calls.Add(1)
		return "value", nil
	}

	// Initial population
	val, err := c.GetOrSet(ctx, "key", factory)
	if err != nil {
		t.Fatalf("initial GetOrSet failed: %v", err)
	}
	if val != "value" {
		t.Fatalf("expected value, got %v", val)
	}
	if calls.Load() != 1 {
		t.Fatalf("expected 1 call, got %d", calls.Load())
	}

	// Advance to 91% of duration (past threshold)
	clk.Advance(9*time.Minute + 6*time.Second)

	// This hit should trigger eager refresh in background
	val2, err2 := c.GetOrSet(ctx, "key", factory)
	if err2 != nil {
		t.Fatal(err2)
	}
	if val2 != "value" {
		t.Fatalf("expected value, got %v", val2)
	}

	// Wait for background goroutine
	time.Sleep(100 * time.Millisecond)

	// Factory should have been called exactly twice (initial + eager refresh)
	if calls.Load() != 2 {
		t.Fatalf("expected 2 calls, got %d", calls.Load())
	}
}

func TestEagerRefresh_NoDoubleRefresh(t *testing.T) {
	c, clk := newTestCache(t, WithDefaultEntryOptions(EntryOptions{
		Duration:              10 * time.Minute,
		EagerRefreshThreshold: 0.9,
		FactoryHardTimeout:    1 * time.Second,
	}))
	ctx := context.Background()

	var calls atomic.Int32
	factory := func(ctx context.Context) (any, error) {
		calls.Add(1)
		time.Sleep(200 * time.Millisecond) // slow refresh
		return "value", nil
	}

	c.GetOrSet(ctx, "key", factory)
	clk.Advance(9*time.Minute + 6*time.Second)

	// Two concurrent hits in the eager zone, should only start one refresh
	c.GetOrSet(ctx, "key", factory)
	c.GetOrSet(ctx, "key", factory)

	time.Sleep(300 * time.Millisecond)

	// Initial + exactly 1 refresh = 2
	if calls.Load() != 2 {
		t.Fatalf("expected 2 calls (no double refresh), got %d", calls.Load())
	}
}
