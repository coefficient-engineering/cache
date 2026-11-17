package cache_test

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coefficient-engineering/cache"
	memorybp "github.com/coefficient-engineering/cache/adapters/backplane/memory"
	syncl1 "github.com/coefficient-engineering/cache/adapters/l1/syncmap"
	"github.com/coefficient-engineering/cache/l1"
)

// Pause a real L1 read after taking its snapshot. Values remain opaque;
// replacement writes and assertions go through the public cache API.
type heldTagLookup struct {
	l1.Adapter
	cancelled <-chan struct{}
	armed     atomic.Bool
	loaded    chan struct{}
	release   <-chan struct{}
}

func (a *heldTagLookup) Get(key string) (any, bool) {
	value, found := a.Adapter.Get(key)
	if key == "tenant:value" && a.armed.CompareAndSwap(true, false) {
		close(a.loaded)
		select {
		case <-a.release:
		case <-a.cancelled:
		}
	}
	return value, found
}

func TestRemoteTagInvalidation_ConcurrentRetagPreservesUnrelatedReplacement(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	hub := memorybp.NewHub()
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	adapter := &heldTagLookup{Adapter: syncl1.New(), cancelled: ctx.Done(), loaded: make(chan struct{}), release: release}
	a := newRemoteTagNode(t, memorybp.NewWithHub("a", hub), "a")
	b := newRemoteTagNode(t, memorybp.NewWithHub("b", hub), "b", cache.WithL1(adapter))
	seedRemoteTagValue(t, b, "value", "red")
	delivered := observeTagDelivery(t, b)
	adapter.armed.Store(true)
	if err := a.DeleteByTag(ctx, "red"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-adapter.loaded:
	case <-ctx.Done():
		t.Fatal("invalidation did not inspect the tagged entry")
	}
	written := make(chan error, 1)
	go func() {
		written <- b.Set(ctx, "value", "green-only", cache.WithTags("green"), func(o *cache.EntryOptions) { o.SkipBackplaneNotifications = true })
	}()
	// An unsynchronized replacement completes while the old snapshot is held.
	// A synchronized one waits until we release it. Bound this progress gate;
	// actual backplane completion is still observed through its receipt event.
	progress := time.NewTimer(time.Second)
	defer progress.Stop()
	var writeErr error
	completed := false
	select {
	case writeErr = <-written:
		completed = true
	case <-progress.C:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	unblock()
	awaitRemoteTagDelivery(t, delivered)
	if !completed {
		select {
		case writeErr = <-written:
		case <-ctx.Done():
			t.Fatal("replacement did not complete")
		}
	}
	if writeErr != nil {
		t.Fatal(writeErr)
	}
	if value, found, err := b.Get(ctx, "value"); err != nil || !found || value != "green-only" {
		t.Fatalf("red invalidation removed unrelated replacement: (%v,%v,%v)", value, found, err)
	}
	if err := a.DeleteByTag(ctx, "green"); err != nil {
		t.Fatal(err)
	}
	awaitRemoteTagDelivery(t, delivered)
	if _, found, err := b.Get(ctx, "value"); err != nil || found {
		t.Fatalf("replacement's own tag: found=%v err=%v", found, err)
	}
}
