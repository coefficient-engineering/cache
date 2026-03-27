package cache

import (
	"context"
	"errors"
	"testing"
	"time"

	memoryl2 "github.com/coefficient-engineering/cache/adapters/l2/memory"
	jsonserializer "github.com/coefficient-engineering/cache/adapters/serializer/json"
)

func TestL2_WriteAndReadThrough(t *testing.T) {
	l2 := memoryl2.New()
	c, _ := newTestCache(t,
		WithL2(l2),
		WithSerializer(&jsonserializer.Serializer{}),
		WithDefaultEntryOptions(EntryOptions{
			Duration: 5 * time.Minute,
		}),
	)
	ctx := context.Background()

	// Write through L1+L2
	c.Set(ctx, "key", "value")

	// Clear L1 to force L2 read
	c.(*cache).l1.Range(func(key, _ any) bool {
		c.(*cache).l1.Delete(key)
		return true
	})

	// Should read from L2 and promote to L1
	val, ok, err := c.Get(ctx, "key")
	if err != nil {
		t.Fatal(err)
	}
	if !ok || val != "value" {
		t.Fatalf("expected value from L2, got ok=%v val=%v", ok, val)
	}
}

func TestL2_FailSafe_StaleFromL2(t *testing.T) {
	l2 := memoryl2.New()
	c, clk := newTestCache(t,
		WithL2(l2),
		WithSerializer(&jsonserializer.Serializer{}),
		WithDefaultEntryOptions(EntryOptions{
			Duration:                 1 * time.Minute,
			IsFailSafeEnabled:        true,
			FailSafeMaxDuration:      1 * time.Hour,
			FailSafeThrottleDuration: 30 * time.Second,
		}),
	)
	ctx := context.Background()

	// Populate L1+L2
	_, err := c.GetOrSet(ctx, "key", func(ctx context.Context) (any, error) {
		return "original", nil
	})
	if err != nil {
		t.Fatalf("failed to populate cache: %v", err)
	}

	// Clear L1, advance time past logical expiry
	c.(*cache).l1.Range(func(key, _ any) bool {
		c.(*cache).l1.Delete(key)
		return true
	})
	clk.Advance(2 * time.Minute)

	// Factory fails, should get stale from L2 via fail-safe
	val, err := c.GetOrSet(ctx, "key", failingFactory(errors.New("db down")))
	if err != nil {
		t.Fatalf("expected fail-safe from L2, got error: %v", err)
	}
	if val != "original" {
		t.Fatalf("expected stale value, got %v", val)
	}
}
