package cache

import (
	"context"
	"testing"
	"time"

	memoryl2 "github.com/coefficient-engineering/cache/adapters/l2/memory"
	jsonserializer "github.com/coefficient-engineering/cache/adapters/serializer/json"
)

// slowL2 delays every Get by delay (or until its context ends) and reports
// each Get's context ending on done.
type slowL2 struct {
	*memoryl2.Adapter
	delay time.Duration
	done  chan error
}

func (s *slowL2) Get(ctx context.Context, key string) ([]byte, error) {
	select {
	case <-time.After(s.delay):
		s.done <- nil
		return s.Adapter.Get(ctx, key)
	case <-ctx.Done():
		s.done <- ctx.Err()
		return nil, ctx.Err()
	}
}

const (
	l2Soft = 20 * time.Millisecond
	l2Slow = 2 * time.Second
)

// newSlowL2Cache returns a cache whose L2 Get takes l2Slow, with key "k"
// stale in L1 (and in L2) and fail-safe enabled.
func newSlowL2Cache(t *testing.T) (Cache, *slowL2) {
	t.Helper()
	l2 := &slowL2{Adapter: memoryl2.New(), delay: l2Slow, done: make(chan error, 4)}
	c, clk := newTestCache(t,
		WithL2(l2),
		WithSerializer(&jsonserializer.Serializer{}),
		WithDefaultEntryOptions(EntryOptions{
			Duration:                 time.Minute,
			IsFailSafeEnabled:        true,
			FailSafeMaxDuration:      time.Hour,
			FailSafeThrottleDuration: time.Second,
		}),
	)
	if err := c.Set(context.Background(), "k", "old"); err != nil {
		t.Fatal(err)
	}
	clk.Advance(2 * time.Minute)
	return c, l2
}

func TestGetOrSet_L2SoftTimeoutSkipsSlowReadWhenStaleAvailable(t *testing.T) {
	c, l2 := newSlowL2Cache(t)
	factory, calls := factoryCounter("new")

	start := time.Now()
	v, err := c.GetOrSet(context.Background(), "k", factory, WithDistributedCacheTimeouts(l2Soft, 0))
	elapsed := time.Since(start)

	if err != nil || v != "new" || calls.Load() != 1 {
		t.Fatalf("GetOrSet = (%v, %v) with %d factory calls, want (new, nil) with 1", v, err, calls.Load())
	}
	if elapsed >= l2Slow/2 {
		t.Fatalf("GetOrSet took %v, want well under the %v L2 read", elapsed, l2Slow)
	}
	// The abandoned read is cancelled, not leaked.
	select {
	case err := <-l2.done:
		if err == nil {
			t.Fatal("abandoned L2 read ran to completion instead of being cancelled")
		}
	case <-time.After(time.Second):
		t.Fatal("abandoned L2 read was not cancelled")
	}
}

func TestGetOrSet_L2SoftTimeoutThenFailingFactoryServesStale(t *testing.T) {
	c, _ := newSlowL2Cache(t)
	v, err := c.GetOrSet(context.Background(), "k", failingFactory(errFactory), WithDistributedCacheTimeouts(l2Soft, 0))
	if err != nil || v != "old" {
		t.Fatalf("GetOrSet = (%v, %v), want stale (old, nil)", v, err)
	}
}

func TestGetOrSet_L2SoftTimeoutIgnoredWithoutStale(t *testing.T) {
	c, l2 := newSlowL2Cache(t)
	l2.delay = 100 * time.Millisecond
	factory, calls := factoryCounter("new")
	// No fail-safe means no fallback: the cache must wait for L2.
	noFailSafe := func(o *EntryOptions) { o.IsFailSafeEnabled = false }
	v, err := c.GetOrSet(context.Background(), "k", factory, noFailSafe, WithDistributedCacheTimeouts(l2Soft, 0))
	if err != nil || v != "new" || calls.Load() != 1 {
		t.Fatalf("GetOrSet = (%v, %v) with %d calls, want (new, nil) with 1", v, err, calls.Load())
	}
	if got := <-l2.done; got != nil {
		t.Fatalf("L2 read was cut short (%v) although no fallback existed", got)
	}
}
