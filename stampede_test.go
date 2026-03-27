package cache

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestStampede_SingleFactory(t *testing.T) {
	c, _ := newTestCache(t)
	ctx := context.Background()

	var callCount atomic.Int32
	factory := func(ctx context.Context) (any, error) {
		callCount.Add(1)
		time.Sleep(50 * time.Millisecond) // simulate work
		return "result", nil
	}

	// Launch 10 goroutines simultaneously
	var wg sync.WaitGroup
	results := make([]any, 10)
	for i := range 10 {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			val, err := c.GetOrSet(ctx, "key", factory)
			if err != nil {
				t.Errorf("goroutine %d: %v", idx, err)
				return
			}
			results[idx] = val
		}(i)
	}
	wg.Wait()

	// Factory should have been called exactly once
	if callCount.Load() != 1 {
		t.Fatalf("factory called %d times, want 1", callCount.Load())
	}

	// All goroutines should have received the same result
	for i, r := range results {
		if r != "result" {
			t.Errorf("goroutine %d got %v, want result", i, r)
		}
	}
}
