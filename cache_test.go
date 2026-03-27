package cache

import (
	"context"
	"testing"
	"time"
)

func TestGetOrSet_BasicHitMiss(t *testing.T) {
	c, _ := newTestCache(t)
	ctx := context.Background()
	factory, count := factoryCounter("hello")
	// first call, miss -> factory called
	val, err := c.GetOrSet(ctx, "key", factory, WithDuration(5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if val != "hello" {
		t.Fatalf("got %v, want hello", val)
	}
	if count.Load() != 1 {
		t.Fatalf("factory called %d times, want 1", count.Load())
	}

	// Second call, hit -> factory NOT called
	val, err = c.GetOrSet(ctx, "key", factory, WithDuration(5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if val != "hello" {
		t.Fatalf("got %v, want hello", val)
	}
	if count.Load() != 1 {
		t.Fatalf("factory called %d times, want 1", count.Load())
	}
}

func TestGet_ReturnsFalseOnMiss(t *testing.T) {
	c, _ := newTestCache(t)
	ctx := context.Background()

	val, ok, err := c.Get(ctx, "nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expected ok=false for missing key")
	}
	if val != nil {
		t.Fatalf("expected nil value, got %v", val)
	}
}

func TestSet_And_Get(t *testing.T) {
	c, _ := newTestCache(t)
	ctx := context.Background()

	err := c.Set(ctx, "key", "world", WithDuration(5*time.Minute))
	if err != nil {
		t.Fatal(err)
	}

	val, ok, err := c.Get(ctx, "key")
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("expected ok=true")
	}
	if val != "world" {
		t.Fatalf("got %v, want world", val)
	}
}

func TestDelete_RemovesEntry(t *testing.T) {
	c, _ := newTestCache(t)
	ctx := context.Background()

	c.Set(ctx, "key", "value")
	c.Delete(ctx, "key")

	_, ok, _ := c.Get(ctx, "key")
	if ok {
		t.Fatal("expected key to be deleted")
	}
}

func TestExpiry_MockClock(t *testing.T) {
	c, clk := newTestCache(t)
	ctx := context.Background()

	c.Set(ctx, "key", "value", WithDuration(5*time.Minute))

	// Still fresh
	_, ok, _ := c.Get(ctx, "key")
	if !ok {
		t.Fatal("expected hit before expiry")
	}

	// Advance past expiry
	clk.Advance(6 * time.Minute)
	_, ok, _ = c.Get(ctx, "key")
	if ok {
		t.Fatal("expected miss after expiry")
	}
}
