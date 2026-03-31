package cache

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Factory changes Duration from the default 5m to 30s.
// The L1 entry's logicalExpiry must reflect the adapted duration.
func TestAdaptiveCaching_Duration(t *testing.T) {
	c, clk := newTestCache(t)
	ctx := context.Background()

	_, err := c.GetOrSet(ctx, "key",
		func(ctx context.Context, fctx *FactoryExecutionContext) (any, error) {
			fctx.Options.Duration = 30 * time.Second
			return "value", nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	// The entry should be fresh at now+29s but stale at now+31s.
	clk.Advance(29 * time.Second)
	val, ok, _ := c.Get(ctx, "key")
	if !ok || val != "value" {
		t.Fatalf("expected fresh hit at 29s, got ok=%v val=%v", ok, val)
	}

	clk.Advance(2 * time.Second) // total 31s
	_, ok, _ = c.Get(ctx, "key")
	if ok {
		t.Fatal("expected miss at 31s — adapted Duration should be 30s, not the default 5m")
	}
}

// Factory adds tags that were not in the original EntryOptions.
func TestAdaptiveCaching_Tags(t *testing.T) {
	c, _ := newTestCache(t)
	ctx := context.Background()

	_, err := c.GetOrSet(ctx, "key",
		func(ctx context.Context, fctx *FactoryExecutionContext) (any, error) {
			fctx.Options.Tags = []string{"dynamic-tag"}
			return "value", nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	// DeleteByTag should remove the entry.
	c.DeleteByTag(ctx, "dynamic-tag")

	_, ok, _ := c.Get(ctx, "key")
	if ok {
		t.Fatal("expected entry to be deleted by dynamically-added tag")
	}
}

// When a stale value exists, the factory receives it via fctx.
func TestAdaptiveCaching_StaleValue(t *testing.T) {
	c, clk := newTestCache(t, WithDefaultEntryOptions(EntryOptions{
		Duration:                 1 * time.Minute,
		IsFailSafeEnabled:        true,
		FailSafeMaxDuration:      1 * time.Hour,
		FailSafeThrottleDuration: 30 * time.Second,
	}))
	ctx := context.Background()

	// Populate initial value.
	_, err := c.GetOrSet(ctx, "key",
		func(ctx context.Context, fctx *FactoryExecutionContext) (any, error) {
			if fctx.HasStaleValue {
				t.Fatal("first call should not have a stale value")
			}
			return "v1", nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}

	// Advance past logical expiry so the entry becomes stale.
	clk.Advance(2 * time.Minute)

	val, err := c.GetOrSet(ctx, "key",
		func(ctx context.Context, fctx *FactoryExecutionContext) (any, error) {
			if !fctx.HasStaleValue {
				t.Fatal("expected HasStaleValue to be true")
			}
			if fctx.StaleValue != "v1" {
				t.Fatalf("expected stale value v1, got %v", fctx.StaleValue)
			}
			// Use stale value to decide Duration.
			fctx.Options.Duration = 10 * time.Minute
			return "v2", nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if val != "v2" {
		t.Fatalf("expected v2, got %v", val)
	}
}

// Factory sets SkipL1Write — value is returned but not stored in L1.
func TestAdaptiveCaching_SkipL1Write(t *testing.T) {
	c, _ := newTestCache(t)
	ctx := context.Background()

	val, err := c.GetOrSet(ctx, "key",
		func(ctx context.Context, fctx *FactoryExecutionContext) (any, error) {
			fctx.Options.SkipL1Write = true
			return "ephemeral", nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if val != "ephemeral" {
		t.Fatalf("expected ephemeral, got %v", val)
	}

	// L1 should not contain the entry.
	_, ok, _ := c.Get(ctx, "key")
	if ok {
		t.Fatal("expected L1 miss after factory set SkipL1Write")
	}
}

// When the factory returns an error, adapted options are discarded
// (storeSafely is never called).
func TestAdaptiveCaching_ErrorDiscardsAdaptations(t *testing.T) {
	c, _ := newTestCache(t)
	ctx := context.Background()

	dbErr := errors.New("db down")
	_, err := c.GetOrSet(ctx, "key",
		func(ctx context.Context, fctx *FactoryExecutionContext) (any, error) {
			fctx.Options.Duration = 1 * time.Hour // would be used if stored
			return nil, dbErr
		},
	)
	if !errors.Is(err, dbErr) {
		t.Fatalf("expected db error, got %v", err)
	}

	// Nothing should be cached.
	_, ok, _ := c.Get(ctx, "key")
	if ok {
		t.Fatal("expected no cached entry after factory error")
	}
}

// A factory that ignores fctx entirely still works.
func TestAdaptiveCaching_IgnoredContext(t *testing.T) {
	c, clk := newTestCache(t, WithDefaultEntryOptions(EntryOptions{
		Duration: 5 * time.Minute,
	}))
	ctx := context.Background()

	val, err := c.GetOrSet(ctx, "key",
		func(ctx context.Context, fctx *FactoryExecutionContext) (any, error) {
			// fctx is intentionally unused.
			return "plain", nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if val != "plain" {
		t.Fatalf("expected plain, got %v", val)
	}

	// Default duration should apply — fresh at 4m, stale at 6m.
	clk.Advance(4 * time.Minute)
	val, ok, _ := c.Get(ctx, "key")
	if !ok || val != "plain" {
		t.Fatal("expected fresh hit at 4m with default duration")
	}

	clk.Advance(2 * time.Minute) // total 6m
	_, ok, _ = c.Get(ctx, "key")
	if ok {
		t.Fatal("expected miss at 6m with 5m default duration")
	}
}
