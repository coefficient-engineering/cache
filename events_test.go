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
	c.GetOrSet(ctx, "key", func(ctx context.Context, fctx *FactoryExecutionContext) (any, error) {
		return "value", nil
	})

	// Hit
	c.GetOrSet(ctx, "key", func(ctx context.Context, fctx *FactoryExecutionContext) (any, error) {
		return "value", nil
	})

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

	var callCount int64
	unsub := c.Events().On(func(e Event) {
		atomic.AddInt64(&callCount, 1)
	})

	c.Set(ctx, "key", "value")
	before := atomic.LoadInt64(&callCount)

	unsub()

	c.Set(ctx, "key2", "value2")
	after := atomic.LoadInt64(&callCount)

	if after != before {
		t.Errorf("handler called after unsubscribe: before=%d after=%d", before, after)
	}
}
