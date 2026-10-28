package cache

import (
	"context"
	"errors"
	"testing"
	"time"

	memoryl2 "github.com/coefficient-engineering/cache/adapters/l2/memory"
	jsonserializer "github.com/coefficient-engineering/cache/adapters/serializer/json"
)

var errFactory = errors.New("factory down")

func failSafeDefaults() EntryOptions {
	return EntryOptions{
		Duration:                       time.Minute,
		IsFailSafeEnabled:              true,
		FailSafeMaxDuration:            time.Hour,
		FailSafeThrottleDuration:       time.Second,
		ReThrowSerializationExceptions: true,
	}
}

// twoNodesOnL2 returns two caches sharing one in-memory L2.
func twoNodesOnL2(t *testing.T, defaults EntryOptions) (a, b Cache, l2 *memoryl2.Adapter) {
	t.Helper()
	l2 = memoryl2.New()
	mk := func() Cache {
		c, err := New(WithL2(l2), WithSerializer(&jsonserializer.Serializer{}), WithDefaultEntryOptions(defaults))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
		return c
	}
	return mk(), mk(), l2
}

func TestExpire_NextGetOrSetCallsFactoryWithL2(t *testing.T) {
	ctx := context.Background()
	for name, defaults := range map[string]EntryOptions{
		"fail-safe off": {Duration: time.Minute},
		"fail-safe on":  failSafeDefaults(),
	} {
		t.Run(name, func(t *testing.T) {
			a, b, _ := twoNodesOnL2(t, defaults)
			if err := a.Set(ctx, "k", "old"); err != nil {
				t.Fatal(err)
			}
			if err := a.Expire(ctx, "k"); err != nil {
				t.Fatal(err)
			}
			for node, c := range map[string]Cache{"same node": a, "other node": b} {
				factory, calls := factoryCounter("new")
				v, err := c.GetOrSet(ctx, "k", factory)
				if err != nil || v != "new" || calls.Load() != 1 {
					t.Fatalf("%s: GetOrSet = (%v, %v) with %d factory calls, want (new, nil) with 1", node, v, err, calls.Load())
				}
				if err := a.Expire(ctx, "k"); err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestExpire_FailSafeKeepsL2ValueAsFallback(t *testing.T) {
	ctx := context.Background()
	a, b, _ := twoNodesOnL2(t, failSafeDefaults())
	if err := a.Set(ctx, "k", "old"); err != nil {
		t.Fatal(err)
	}
	if err := a.Expire(ctx, "k"); err != nil {
		t.Fatal(err)
	}
	// b has nothing in L1: the fallback must come from L2.
	v, err := b.GetOrSet(ctx, "k", failingFactory(errFactory))
	if err != nil || v != "old" {
		t.Fatalf("GetOrSet = (%v, %v), want stale (old, nil)", v, err)
	}
}

func TestExpire_WithoutFailSafeRemovesL2Entry(t *testing.T) {
	ctx := context.Background()
	a, b, l2 := twoNodesOnL2(t, EntryOptions{Duration: time.Minute})
	if err := a.Set(ctx, "k", "old"); err != nil {
		t.Fatal(err)
	}
	if err := a.Expire(ctx, "k"); err != nil {
		t.Fatal(err)
	}
	if data, _ := l2.Get(ctx, "k"); data != nil {
		t.Fatalf("L2 still holds %q after Expire without fail-safe", data)
	}
	if _, err := b.GetOrSet(ctx, "k", failingFactory(errFactory)); !errors.Is(err, errFactory) {
		t.Fatalf("GetOrSet error = %v, want factory error", err)
	}
}

func TestExpire_SkipL2WriteLeavesL2(t *testing.T) {
	ctx := context.Background()
	a, b, _ := twoNodesOnL2(t, EntryOptions{Duration: time.Minute})
	if err := a.Set(ctx, "k", "old"); err != nil {
		t.Fatal(err)
	}
	if err := a.Expire(ctx, "k", func(o *EntryOptions) { o.SkipL2Write = true }); err != nil {
		t.Fatal(err)
	}
	if v, ok, err := b.Get(ctx, "k"); err != nil || !ok || v != "old" {
		t.Fatalf("Get on other node = (%v, %v, %v), want (old, true, nil)", v, ok, err)
	}
}

func TestExpire_ReturnsL2ErrorWhenRethrow(t *testing.T) {
	ctx := context.Background()
	for name, opt := range map[string]EntryOption{
		"fail-safe off": func(*EntryOptions) {},
		"fail-safe on":  WithFailSafe(time.Hour, time.Second),
	} {
		t.Run(name, func(t *testing.T) {
			c := newFailingL2Cache(t, WithL2CircuitBreaker(1, time.Hour))
			_ = c.Set(ctx, "k", "v") // opens the breaker
			if err := c.Expire(ctx, "k", opt, rethrowL2); !errors.Is(err, errL2) {
				t.Fatalf("Expire error = %v, want wrapping %v (breaker must not gate Expire)", err, errL2)
			}
			if err := c.Expire(ctx, "k", opt); err != nil {
				t.Fatalf("Expire without rethrow error = %v, want nil", err)
			}
		})
	}
}
