package cache

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
)

func TestEvents_HitAndMiss(t *testing.T) {
	c, _ := newTestCache(t)
	ctx := context.Background()

	var mu sync.Mutex
	var events []Event

	c.Events().On(func(e Event) {
		mu.Lock()
		events = append(events, e)
		mu.Unlock()
	})

	// Miss -> factory -> success -> set
	_, err := c.GetOrSet(ctx, "key", func(_ context.Context, _ *FactoryExecutionContext) (any, error) {
		return "value", nil
	})
	if err != nil {
		t.Fatal(err)
	}

	// Hit
	_, err = c.GetOrSet(ctx, "key", func(_ context.Context, _ *FactoryExecutionContext) (any, error) {
		return "value", nil
	})
	if err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()

	// First call should emit: CacheMiss, FactoryCall, FactorySuccess
	// Second call should emit: CacheHit
	hasHit := false
	hasMiss := false
	for _, e := range events {
		switch e.(type) {
		case EventCacheHit:
			hasHit = true
		case EventCacheMiss:
			hasMiss = true
		}
	}
	if !hasHit {
		t.Error("expected at least one CacheHit event")
	}
	if !hasMiss {
		t.Error("expected at least one CacheMiss event")
	}
}

func TestEvents_Unsubscribe(t *testing.T) {
	c, _ := newTestCache(t)
	ctx := context.Background()

	var callCount atomic.Int64
	unsub := c.Events().On(func(_ Event) {
		callCount.Add(1)
	})

	if err := c.Set(ctx, "key", "value"); err != nil {
		t.Fatal(err)
	}
	before := callCount.Load()

	unsub()

	if err := c.Set(ctx, "key2", "value2"); err != nil {
		t.Fatal(err)
	}
	after := callCount.Load()

	if after != before {
		t.Errorf("handler called after unsubscribe: before=%d after=%d", before, after)
	}
}
