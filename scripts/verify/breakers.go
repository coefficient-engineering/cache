package main

import (
	"context"
	"errors"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coefficient-engineering/cache"
	jsonserializer "github.com/coefficient-engineering/cache/adapters/serializer/json"
	"github.com/coefficient-engineering/cache/backplane"
	"github.com/coefficient-engineering/cache/l2"
)

var errBackendUnavailable = errors.New("backend unavailable")

type transitionL2 struct{ calls atomic.Int32 }

func (a *transitionL2) Get(context.Context, string) ([]byte, error) {
	if a.calls.Add(1) == 1 {
		return nil, errBackendUnavailable
	}
	return nil, nil
}

func (*transitionL2) Set(context.Context, string, []byte, time.Duration, []string) error { return nil }

func (*transitionL2) Delete(context.Context, string) error { return nil }

func (*transitionL2) DeleteByTag(context.Context, string) error { return nil }

func (*transitionL2) Clear(context.Context) error { return nil }

func (*transitionL2) Ping(context.Context) error { return nil }

var _ l2.Adapter = (*transitionL2)(nil)

type transitionBackplane struct{ calls atomic.Int32 }

func (b *transitionBackplane) Publish(context.Context, backplane.Message) error {
	if b.calls.Add(1) == 1 {
		return errBackendUnavailable
	}
	return nil
}
func (*transitionBackplane) Subscribe(backplane.Handler) (func(), error) { return func() {}, nil }
func (*transitionBackplane) Close() error                                { return nil }

var _ backplane.Backplane = (*transitionBackplane)(nil)

// runBreakerEvents proves breaker event transitions for both cache backends.
func runBreakerEvents(ctx context.Context, r *report) error {
	defaults := cache.EntryOptions{Duration: time.Minute, AllowBackgroundBackplaneOperations: false}
	c, err := cache.New(
		cache.WithL2(&transitionL2{}),
		cache.WithSerializer(&jsonserializer.Serializer{}),
		cache.WithL2CircuitBreaker(1, time.Millisecond),
		cache.WithBackplane(&transitionBackplane{}),
		cache.WithBackplaneCircuitBreaker(1, time.Millisecond),
		cache.WithDefaultEntryOptions(defaults),
	)
	if err != nil {
		return err
	}
	defer c.Close()

	var mu sync.Mutex
	l2Events, bpEvents := []bool{}, []bool{}
	c.Events().On(func(event cache.Event) {
		mu.Lock()
		defer mu.Unlock()
		switch change := event.(type) {
		case cache.EventL2CircuitBreakerStateChange:
			l2Events = append(l2Events, change.Open)
		case cache.EventBackplaneCircuitBreakerStateChange:
			bpEvents = append(bpEvents, change.Open)
		}
	})
	_, _ = cache.GetOrSet(ctx, c, "breaker", func(context.Context, *cache.FactoryExecutionContext) (string, error) { return "value", nil })
	time.Sleep(2 * time.Millisecond)
	if _, _, err := c.Get(ctx, "probe"); err != nil {
		return err
	}
	if err := c.Set(ctx, "probe", "value"); err != nil {
		return err
	}

	_, found, getErr := c.Get(ctx, "breaker")
	r.record(check{Name: "backplane-recovery-clears-l1", OK: getErr == nil && !found, Observed: map[string]any{foundKey: found, errorKey: errString(getErr)}})
	mu.Lock()
	defer mu.Unlock()
	l2OK := slices.Equal(l2Events, []bool{true, false})
	bpOK := slices.Equal(bpEvents, []bool{true, false})
	r.record(check{Name: "l2-breaker-transitions", OK: l2OK, Observed: l2Events})
	r.record(check{Name: "backplane-breaker-transitions", OK: bpOK, Observed: bpEvents})
	return nil
}
