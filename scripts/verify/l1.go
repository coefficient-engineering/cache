package main

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coefficient-engineering/cache"
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
