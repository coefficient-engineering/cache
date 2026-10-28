package cache

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	l2memory "github.com/coefficient-engineering/cache/adapters/l2/memory"
	jsonserializer "github.com/coefficient-engineering/cache/adapters/serializer/json"
)

func TestGetOrSetFactorySuccessSharedEvents(t *testing.T) {
	c, _ := newTestCache(t)
	const callers = 8
	start := make(chan struct{})
	var wg sync.WaitGroup
	var mu sync.Mutex
	shared, unshared := 0, 0
	c.Events().On(func(event Event) {
		success, ok := event.(EventFactorySuccess)
		if !ok {
			return
		}
		mu.Lock()
		if success.Shared {
			shared++
		} else {
			unshared++
		}
		mu.Unlock()
	})
	wg.Add(callers)
	for range callers {
		go func() {
			defer wg.Done()
			<-start
			_, err := c.GetOrSet(context.Background(), "shared-event", func(context.Context, *FactoryExecutionContext) (any, error) {
				time.Sleep(50 * time.Millisecond)
				return "value", nil
			})
			if err != nil {
				t.Errorf("GetOrSet: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	if shared != callers-1 || unshared != 1 {
		t.Fatalf("factory success events: Shared=true %d, false %d; want %d and 1", shared, unshared, callers-1)
	}
}

func TestGetOrSetSkipL2ReadWhenStale(t *testing.T) {
	for _, skipL2 := range []bool{false, true} {
		t.Run(fmt.Sprintf("skip-l2-%t", skipL2), func(t *testing.T) {
			adapter := l2memory.New()
			opts := []Option{
				WithL2(adapter),
				WithSerializer(&jsonserializer.Serializer{}),
			}
			staleCache, err := New(opts...)
			if err != nil {
				t.Fatal(err)
			}
			defer staleCache.Close()
			freshCache, err := New(opts...)
			if err != nil {
				t.Fatal(err)
			}
			defer freshCache.Close()

			if err := staleCache.Set(context.Background(), "key", "stale", WithDuration(30*time.Millisecond)); err != nil {
				t.Fatal(err)
			}
			if err := freshCache.Set(context.Background(), "key", "fresh", WithDuration(time.Minute)); err != nil {
				t.Fatal(err)
			}
			time.Sleep(40 * time.Millisecond)

			l2Hits := 0
			staleCache.Events().On(func(event Event) {
				if _, ok := event.(EventL2Hit); ok {
					l2Hits++
				}
			})
			var factoryCalls int
			entryOpts := []EntryOption{WithDuration(time.Minute)}
			if skipL2 {
				entryOpts = append(entryOpts, WithSkipL2ReadWhenStale())
			}
			got, err := staleCache.GetOrSet(context.Background(), "key", func(context.Context, *FactoryExecutionContext) (any, error) {
				factoryCalls++
				return "factory", nil
			}, entryOpts...)
			if err != nil {
				t.Fatal(err)
			}
			if skipL2 {
				if got != "factory" || factoryCalls != 1 || l2Hits != 0 {
					t.Fatalf("got %v, factory calls %d, L2 hits %d; want factory/1/0", got, factoryCalls, l2Hits)
				}
			} else if got != "fresh" || factoryCalls != 0 || l2Hits != 1 {
				t.Fatalf("got %v, factory calls %d, L2 hits %d; want fresh/0/1", got, factoryCalls, l2Hits)
			}
		})
	}
}

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

	if err := c.Set(ctx, "key", "value"); err != nil {
		t.Fatal(err)
	}
	if err := c.Delete(ctx, "key"); err != nil {
		t.Fatal(err)
	}

	_, ok, _ := c.Get(ctx, "key")
	if ok {
		t.Fatal("expected key to be deleted")
	}
}

func TestExpiry_MockClock(t *testing.T) {
	c, clk := newTestCache(t)
	ctx := context.Background()

	if err := c.Set(ctx, "key", "value", WithDuration(5*time.Minute)); err != nil {
		t.Fatal(err)
	}

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
