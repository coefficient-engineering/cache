package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coefficient-engineering/cache"
	l2memory "github.com/coefficient-engineering/cache/adapters/l2/memory"
	jsonserializer "github.com/coefficient-engineering/cache/adapters/serializer/json"
)

const (
	concurrentCallers = 50
	slowFactory       = 100 * time.Millisecond
	shortDuration     = 50 * time.Millisecond
	failSafeWindow    = time.Hour
)

var errUpstreamDown = errors.New("upstream down")

// runStampede proves concurrent misses on one key share a single factory call.
func runStampede(ctx context.Context, r *report) error {
	c, err := cache.New()
	if err != nil {
		return err
	}
	defer c.Close()

	var calls atomic.Int32
	factory := func(ctx context.Context, _ *cache.FactoryExecutionContext) (string, error) {
		calls.Add(1)
		time.Sleep(slowFactory)
		return "product-42", ctx.Err()
	}

	var wg sync.WaitGroup
	results := make([]string, concurrentCallers)
	errs := make([]error, concurrentCallers)
	for i := range concurrentCallers {
		wg.Go(func() {
			results[i], errs[i] = cache.GetOrSet(ctx, c, "product:42", factory)
		})
	}
	wg.Wait()

	allSame := true
	for i := range results {
		if errs[i] != nil || results[i] != "product-42" {
			allSame = false
		}
	}
	r.record(check{Name: "factory-called-once", OK: calls.Load() == 1, Observed: map[string]any{"callers": concurrentCallers, "factory_calls": calls.Load()}})
	r.record(check{Name: "all-callers-got-value", OK: allSame, Observed: results[0]})
	return nil
}

// runSharedFactoryEvents proves singleflight waiters are marked Shared.
func runSharedFactoryEvents(ctx context.Context, r *report) error {
	c, err := cache.New()
	if err != nil {
		return err
	}
	defer c.Close()
	const callers = 8
	var mu sync.Mutex
	shared, unshared := 0, 0
	c.Events().On(func(e cache.Event) {
		event, ok := e.(cache.EventFactorySuccess)
		if !ok {
			return
		}
		mu.Lock()
		if event.Shared {
			shared++
		} else {
			unshared++
		}
		mu.Unlock()
	})
	var wg sync.WaitGroup
	for range callers {
		wg.Go(func() {
			_, _ = cache.GetOrSet(ctx, c, "shared-event", func(context.Context, *cache.FactoryExecutionContext) (string, error) {
				time.Sleep(slowFactory)
				return "value", nil
			})
		})
	}
	wg.Wait()
	mu.Lock()
	defer mu.Unlock()
	r.record(check{Name: "factory-success-sharing", OK: unshared == 1 && shared == callers-1, Observed: map[string]int{"shared": shared, "unshared": unshared}})
	return nil
}

// runSkipL2ReadWhenStale proves a stale L1 entry can bypass a newer L2 value.
func runSkipL2ReadWhenStale(ctx context.Context, r *report) error {
	for _, skipL2 := range []bool{false, true} {
		adapter := l2memory.New()
		opts := []cache.Option{
			cache.WithL2(adapter),
			cache.WithSerializer(&jsonserializer.Serializer{}),
		}
		staleCache, err := cache.New(opts...)
		if err != nil {
			return err
		}
		freshCache, err := cache.New(opts...)
		if err != nil {
			_ = staleCache.Close()
			return err
		}
		if err := staleCache.Set(ctx, "key", "stale", cache.WithDuration(shortDuration)); err != nil {
			_ = staleCache.Close()
			_ = freshCache.Close()
			return err
		}
		if err := freshCache.Set(ctx, "key", "fresh", cache.WithDuration(time.Minute)); err != nil {
			_ = staleCache.Close()
			_ = freshCache.Close()
			return err
		}
		time.Sleep(2 * shortDuration)
		events := &eventLog{}
		events.attach(staleCache)
		var calls atomic.Int32
		entryOpts := []cache.EntryOption{cache.WithDuration(time.Minute)}
		if skipL2 {
			entryOpts = append(entryOpts, cache.WithSkipL2ReadWhenStale())
		}
		got, callErr := cache.GetOrSet(ctx, staleCache, "key", func(context.Context, *cache.FactoryExecutionContext) (string, error) {
			calls.Add(1)
			return "factory", nil
		}, entryOpts...)
		l2Hits := events.count("EventL2Hit")
		wantValue, wantCalls, wantHits := "fresh", int32(0), 1
		if skipL2 {
			wantValue, wantCalls, wantHits = "factory", 1, 0
		}
		r.record(check{Name: fmt.Sprintf("skip-l2-on-stale-%t", skipL2), OK: callErr == nil && got == wantValue && calls.Load() == wantCalls && l2Hits == wantHits, Observed: map[string]any{"value": got, "factory_calls": calls.Load(), "l2_hits": l2Hits}})
		_ = staleCache.Close()
		_ = freshCache.Close()
	}
	return nil
}

// runFailSafe proves a failing factory yields the stale value, not an error.
func runFailSafe(ctx context.Context, r *report) error {
	c, err := cache.New()
	if err != nil {
		return err
	}
	defer c.Close()
	events := &eventLog{}
	events.attach(c)

	opts := []cache.EntryOption{cache.WithDuration(shortDuration), cache.WithFailSafe(failSafeWindow, time.Second)}
	if err := c.Set(ctx, "price:7", "9.99", opts...); err != nil {
		return err
	}
	time.Sleep(2 * shortDuration)

	got, err := cache.GetOrSet(ctx, c, "price:7", func(context.Context, *cache.FactoryExecutionContext) (string, error) {
		return "", errUpstreamDown
	}, opts...)
	r.record(check{Name: "stale-value-returned", OK: err == nil && got == "9.99", Observed: map[string]any{valueKey: got, "error": errString(err)}})
	r.record(check{Name: "failsafe-event-emitted", OK: events.count("EventFailSafeActivated") == 1, Observed: events.count("EventFailSafeActivated")})

	_, err = cache.GetOrSet(ctx, c, "price:never-cached", func(context.Context, *cache.FactoryExecutionContext) (string, error) {
		return "", errUpstreamDown
	}, opts...)
	r.record(check{Name: "no-stale-propagates-error", OK: errors.Is(err, errUpstreamDown), Observed: errString(err)})
	return nil
}

// runTags proves DeleteByTag removes tagged entries and leaves others.
func runTags(ctx context.Context, r *report) error {
	c, err := cache.New()
	if err != nil {
		return err
	}
	defer c.Close()

	for key, tag := range map[string]string{"p:1": "featured", "p:2": "featured", "p:3": "clearance"} {
		if err := c.Set(ctx, key, key+"-value", cache.WithTags(tag)); err != nil {
			return err
		}
	}
	if err := c.DeleteByTag(ctx, "featured"); err != nil {
		return err
	}

	present := map[string]bool{}
	for _, key := range []string{"p:1", "p:2", "p:3"} {
		_, ok, err := c.Get(ctx, key)
		if err != nil {
			return err
		}
		present[key] = ok
	}
	r.record(check{Name: "tagged-entries-removed", OK: !present["p:1"] && !present["p:2"], Observed: present})
	r.record(check{Name: "untagged-entry-kept", OK: present["p:3"], Observed: present})
	return nil
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
