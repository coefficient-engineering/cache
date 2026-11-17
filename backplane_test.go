package cache

import (
	"context"
	"testing"
	"time"

	memorybp "github.com/coefficient-engineering/cache/adapters/backplane/memory"
	"github.com/coefficient-engineering/cache/backplane"
)

func TestBackplane_SetInvalidatesRemoteL1(t *testing.T) {
	// Shared hub so bp1 and bp2 can exchange messages
	hub := memorybp.NewHub()
	bp1 := memorybp.NewWithHub("node-1", hub)
	bp2 := memorybp.NewWithHub("node-2", hub)

	c1, _ := newTestCache(t,
		WithBackplane(bp1),
		WithNodeID("node-1"),
	)
	c2, _ := newTestCache(t,
		WithBackplane(bp2),
		WithNodeID("node-2"),
	)
	ctx := context.Background()

	// Both nodes have the key
	if err := c1.Set(ctx, "key", "v1"); err != nil {
		t.Fatal(err)
	}
	if err := c2.Set(ctx, "key", "v1"); err != nil {
		t.Fatal(err)
	}

	// Node 1 updates the key
	if err := c1.Set(ctx, "key", "v2"); err != nil {
		t.Fatal(err)
	}

	// Allow backplane message to propagate
	time.Sleep(50 * time.Millisecond)

	// Node 2's L1 should have been invalidated by the backplane message.
	val, ok, err := c2.Get(ctx, "key")
	if err != nil {
		t.Fatalf("unexpected error from c2.Get: %v", err)
	}
	if ok && val != "v2" {
		t.Fatalf("expected L1 invalidated (ok=false) or updated value 'v2', got ok=%v val=%v", ok, val)
	}
}

func TestBackplane_DeleteByTagsInvalidatesRemoteL1(t *testing.T) {
	hub := memorybp.NewHub()
	bp1 := memorybp.NewWithHub("node-1", hub)
	c1, _ := newTestCache(t,
		WithBackplane(bp1),
		WithNodeID("node-1"),
	)
	c2, _ := newTestCache(t,
		WithBackplane(memorybp.NewWithHub("node-2", hub)),
		WithNodeID("node-2"),
	)
	ctx := context.Background()

	// Seed without notifications so setup cannot invalidate either node.
	withoutNotifications := func(o *EntryOptions) { o.SkipBackplaneNotifications = true }
	entries := []struct {
		key  string
		tags []string
	}{
		{"p1", []string{"featured"}},
		{"p2", []string{"sale"}},
		{"p3", []string{"featured", "sale"}},
		{"p4", []string{"clothing"}},
	}
	for _, c := range []Cache{c1, c2} {
		for _, entry := range entries {
			if err := c.Set(ctx, entry.key, entry.key, WithTags(entry.tags...), withoutNotifications); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, entry := range entries {
		if value, ok, err := c2.Get(ctx, entry.key); err != nil || !ok || value != entry.key {
			t.Fatalf("remote seed %s = (%v, %v, %v), want a hit", entry.key, value, ok, err)
		}
	}

	sent := make(map[string]int)
	c1.Events().On(func(event Event) {
		if event, ok := event.(EventBackplaneSent); ok && event.Type == backplane.MessageTypeDelete {
			sent[event.Key]++
		}
	})
	delivered := make(chan struct{})
	c2.Events().On(func(event Event) {
		if event, ok := event.(EventBackplaneReceived); ok && event.Key == "delivery-barrier" {
			close(delivered)
		}
	})
	synchronous := func(o *EntryOptions) { o.AllowBackgroundBackplaneOperations = false }
	if err := c1.DeleteByTags(ctx, []string{"featured", "sale", "featured"}, synchronous); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"p1", "p2", "p3"} {
		if sent[key] != 1 {
			t.Errorf("delete notifications for %s = %d, want 1", key, sent[key])
		}
	}
	if len(sent) != 3 {
		t.Errorf("delete notifications = %v, want only p1, p2, p3", sent)
	}

	// The memory backplane dispatches in order. Receiving this marker means
	// all preceding delete handlers have finished evicting remote entries.
	if err := bp1.Publish(ctx, backplane.Message{
		Type:      backplane.MessageTypeDelete,
		CacheName: c1.Name(),
		Key:       "delivery-barrier",
	}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-delivered:
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for backplane delivery")
	}
	for _, key := range []string{"p1", "p2", "p3"} {
		if _, ok, err := c2.Get(ctx, key); err != nil || ok {
			t.Errorf("remote %s after invalidation: hit=%v, err=%v", key, ok, err)
		}
	}
	if value, ok, err := c2.Get(ctx, "p4"); err != nil || !ok || value != "p4" {
		t.Errorf("unrelated remote entry = (%v, %v, %v), want (p4, true, nil)", value, ok, err)
	}
}
