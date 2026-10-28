package cache

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	jsonserializer "github.com/coefficient-engineering/cache/adapters/serializer/json"
	"github.com/coefficient-engineering/cache/backplane"
)

var errPublish = errors.New("backplane unreachable")

// failingBackplane rejects every publish.
type failingBackplane struct{}

func (failingBackplane) Publish(context.Context, backplane.Message) error { return errPublish }

func (failingBackplane) Subscribe(backplane.Handler) (func(), error) { return func() {}, nil }

func (failingBackplane) Close() error { return nil }

func syncRethrow(o *EntryOptions) {
	o.AllowBackgroundBackplaneOperations = false
	o.ReThrowBackplaneExceptions = true
}

func TestSet_ReturnsBackplaneErrorWhenSyncAndRethrow(t *testing.T) {
	c, _ := newTestCache(t, WithBackplane(failingBackplane{}))
	ctx := context.Background()

	err := c.Set(ctx, "k", "v", syncRethrow)
	if !errors.Is(err, errPublish) {
		t.Fatalf("Set error = %v, want wrapping %v", err, errPublish)
	}

	// The local write still happened.
	got, ok, err := c.Get(ctx, "k")
	if err != nil || !ok || got != "v" {
		t.Fatalf("Get after failed publish = (%v, %v, %v), want (v, true, nil)", got, ok, err)
	}
}

func TestMutations_ReturnBackplaneErrorWhenSyncAndRethrow(t *testing.T) {
	ctx := context.Background()
	cases := map[string]func(Cache) error{
		"Delete":      func(c Cache) error { return c.Delete(ctx, "k", syncRethrow) },
		"Expire":      func(c Cache) error { return c.Expire(ctx, "k", syncRethrow) },
		"DeleteByTag": func(c Cache) error { return c.DeleteByTag(ctx, "t", syncRethrow) },
		// Clear has no per-call options; it follows the cache defaults.
		"Clear": func(c Cache) error { return c.Clear(ctx, false) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			defaults := EntryOptions{Duration: time.Minute}
			syncRethrow(&defaults)
			c, _ := newTestCache(t, WithBackplane(failingBackplane{}), WithDefaultEntryOptions(defaults))
			// Seed a tagged entry; ignore its own publish error.
			_ = c.Set(ctx, "k", "v", WithTags("t"))

			if err := mutate(c); !errors.Is(err, errPublish) {
				t.Fatalf("%s error = %v, want wrapping %v", name, err, errPublish)
			}
		})
	}
}

func TestMutations_SwallowBackplaneErrorUnlessSyncAndRethrow(t *testing.T) {
	ctx := context.Background()
	cases := map[string]EntryOption{
		"background (default)": func(*EntryOptions) {},
		"sync without rethrow": func(o *EntryOptions) { o.AllowBackgroundBackplaneOperations = false },
		"background with rethrow": func(o *EntryOptions) {
			o.ReThrowBackplaneExceptions = true
		},
	}
	for name, opt := range cases {
		t.Run(name, func(t *testing.T) {
			c, _ := newTestCache(t, WithBackplane(failingBackplane{}))
			if err := c.Set(ctx, "k", "v", opt); err != nil {
				t.Fatalf("Set error = %v, want nil", err)
			}
			if err := c.Delete(ctx, "k", opt); err != nil {
				t.Fatalf("Delete error = %v, want nil", err)
			}
		})
	}
}

type recoveringBackplane struct {
	publishes atomic.Int32
}

func (b *recoveringBackplane) Publish(context.Context, backplane.Message) error {
	if b.publishes.Add(1) == 1 {
		return errPublish
	}
	return nil
}

func (*recoveringBackplane) Subscribe(backplane.Handler) (func(), error) { return func() {}, nil }

func (*recoveringBackplane) Close() error { return nil }

func TestBackplaneCircuitBreakerEmitsTransitions(t *testing.T) {
	defaults := EntryOptions{Duration: time.Minute, AllowBackgroundBackplaneOperations: false}
	bp := &recoveringBackplane{}
	c, err := New(
		WithBackplane(bp),
		WithBackplaneCircuitBreaker(1, time.Millisecond),
		WithDefaultEntryOptions(defaults),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	var mu sync.Mutex
	var transitions []bool
	c.Events().On(func(event Event) {
		if change, ok := event.(EventBackplaneCircuitBreakerStateChange); ok {
			mu.Lock()
			transitions = append(transitions, change.Open)
			mu.Unlock()
		}
	})
	_ = c.Set(context.Background(), "breaker", "value")
	_ = c.Set(context.Background(), "breaker", "value")
	time.Sleep(2 * time.Millisecond)
	_ = c.Set(context.Background(), "breaker", "value")

	if _, ok, err := c.Get(context.Background(), "breaker"); err != nil || ok {
		t.Fatalf("Get after recovery = (ok=%v, err=%v), want miss", ok, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(transitions) != 2 || !transitions[0] || transitions[1] {
		t.Fatalf("transitions = %v, want [true false]", transitions)
	}
}

type recoveringL2 struct {
	gets atomic.Int32
}

func (a *recoveringL2) Get(context.Context, string) ([]byte, error) {
	if a.gets.Add(1) == 1 {
		return nil, errPublish
	}
	return nil, nil
}

func (*recoveringL2) Set(context.Context, string, []byte, time.Duration, []string) error { return nil }

func (*recoveringL2) Delete(context.Context, string) error { return nil }

func (*recoveringL2) DeleteByTag(context.Context, string) error { return nil }

func (*recoveringL2) Clear(context.Context) error { return nil }

func (*recoveringL2) Ping(context.Context) error { return nil }

func TestL2CircuitBreakerEmitsTransitions(t *testing.T) {
	adapter := &recoveringL2{}
	c, err := New(
		WithL2(adapter),
		WithSerializer(&jsonserializer.Serializer{}),
		WithL2CircuitBreaker(1, time.Millisecond),
		WithDefaultEntryOptions(EntryOptions{Duration: time.Minute}),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()

	var mu sync.Mutex
	var transitions []bool
	c.Events().On(func(event Event) {
		if change, ok := event.(EventL2CircuitBreakerStateChange); ok {
			mu.Lock()
			transitions = append(transitions, change.Open)
			mu.Unlock()
		}
	})
	_, _ = c.GetOrSet(context.Background(), "breaker", func(context.Context, *FactoryExecutionContext) (any, error) {
		return "value", nil
	})
	time.Sleep(2 * time.Millisecond)
	if _, _, err := c.Get(context.Background(), "probe"); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(transitions) != 2 || !transitions[0] || transitions[1] {
		t.Fatalf("transitions = %v, want [true false]", transitions)
	}
}
