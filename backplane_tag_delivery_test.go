package cache_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/coefficient-engineering/cache"
	memorybp "github.com/coefficient-engineering/cache/adapters/backplane/memory"
	"github.com/coefficient-engineering/cache/backplane"
)

// Gate an external transport boundary, retaining the real memory dispatch.
// Cache operations and all assertions still use the public cache interface.
type heldTagPublisher struct {
	backplane.Backplane
	publishing chan backplane.Message
	release    <-chan struct{}
}

func (b *heldTagPublisher) Publish(ctx context.Context, msg backplane.Message) error {
	if msg.Type == backplane.MessageTypeDeleteByTags {
		select {
		case b.publishing <- msg:
		case <-ctx.Done():
			return ctx.Err()
		}
		select {
		case <-b.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return b.Backplane.Publish(ctx, msg)
}

func TestRemoteTagInvalidation_DelayedDeliveryAndFreshWrites(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	hub := memorybp.NewHub()
	publisher := memorybp.NewWithHub("a", hub)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	held := &heldTagPublisher{Backplane: publisher, publishing: make(chan backplane.Message, 1), release: release}
	a := newRemoteTagNode(t, held, "a")
	b := newRemoteTagNode(t, memorybp.NewWithHub("b", hub), "b")
	seedRemoteTagValue(t, b, "value", "red")
	delivered := observeTagDelivery(t, b)
	tags := []string{"red", "red"}
	// Defaults return without waiting for the transport, let alone every peer.
	if err := a.DeleteByTags(ctx, tags); err != nil {
		t.Fatal(err)
	}
	var replay backplane.Message
	select {
	case replay = <-held.publishing:
	case <-ctx.Done():
		t.Fatal("publish was not attempted")
	}
	tags[0] = "green"
	tags[1] = "green"
	if value, found, err := b.Get(t.Context(), "value"); err != nil || !found || value != "value" {
		t.Fatalf("before delivery = (%v,%v,%v)", value, found, err)
	}
	// No timestamp fencing: delayed delivery can also remove a newer matching Set.
	if err := b.Set(t.Context(), "value", "replacement", cache.WithTags("red"), func(o *cache.EntryOptions) { o.SkipBackplaneNotifications = true }); err != nil {
		t.Fatal(err)
	}
	unblock()
	awaitRemoteTagDelivery(t, delivered)
	if value, found, err := b.Get(t.Context(), "value"); err != nil || found {
		t.Fatalf("after delayed delivery = (%v,%v,%v)", value, found, err)
	}
	if err := publisher.Publish(t.Context(), replay); err != nil {
		t.Fatal(err)
	}
	awaitRemoteTagDelivery(t, delivered)
	if _, found, err := b.Get(t.Context(), "value"); err != nil || found {
		t.Fatalf("after duplicate delivery: found=%v err=%v", found, err)
	}
	seedRemoteTagValue(t, b, "value", "red")
}

func TestRemoteTagInvalidation_ReceiptObservesCompletedEviction(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	hub := memorybp.NewHub()
	a := newRemoteTagNode(t, memorybp.NewWithHub("a", hub), "a")
	b := newRemoteTagNode(t, memorybp.NewWithHub("b", hub), "b")
	seedRemoteTagValue(t, b, "value", "red")
	observed := make(chan bool, 1)
	t.Cleanup(b.Events().On(func(event cache.Event) {
		if received, ok := event.(cache.EventBackplaneReceived); ok && received.Type == backplane.MessageTypeDeleteByTags {
			_, found, err := b.Get(t.Context(), "value")
			observed <- !found && err == nil
		}
	}))
	if err := a.DeleteByTag(ctx, "red"); err != nil {
		t.Fatal(err)
	}
	select {
	case evicted := <-observed:
		if !evicted {
			t.Fatal("receipt callback observed an entry that should already be evicted")
		}
	case <-ctx.Done():
		t.Fatal("receipt did not arrive")
	}
}

func TestRemoteTagInvalidation_PreservesRetaggedEntries(t *testing.T) {
	hub := memorybp.NewHub()
	a := newRemoteTagNode(t, memorybp.NewWithHub("a", hub), "a")
	b := newRemoteTagNode(t, memorybp.NewWithHub("b", hub), "b")
	seedRemoteTagValue(t, b, "value", "red")
	seedRemoteTagValue(t, b, "value", "green")
	delivered := observeTagDelivery(t, b)
	if err := a.DeleteByTag(t.Context(), "red"); err != nil {
		t.Fatal(err)
	}
	awaitRemoteTagDelivery(t, delivered)
	if value, found, err := b.Get(t.Context(), "value"); err != nil || !found || value != "value" {
		t.Fatalf("retagged replacement = (%v,%v,%v)", value, found, err)
	}
	if err := a.DeleteByTag(t.Context(), "green"); err != nil {
		t.Fatal(err)
	}
	awaitRemoteTagDelivery(t, delivered)
	if _, found, err := b.Get(t.Context(), "value"); err != nil || found {
		t.Fatalf("replacement's own tag: found=%v err=%v", found, err)
	}
}
