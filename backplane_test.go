package cache

import (
	"context"
	"testing"
	"time"

	memorybp "github.com/coefficient-engineering/cache/adapters/backplane/memory"
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
	c1, _ := newTestCache(t,
		WithBackplane(memorybp.NewWithHub("node-1", hub)),
		WithNodeID("node-1"),
	)
	c2, _ := newTestCache(t,
		WithBackplane(memorybp.NewWithHub("node-2", hub)),
		WithNodeID("node-2"),
	)
	ctx := context.Background()

	// Both nodes hold the tagged keys in L1.
	for _, c := range []Cache{c1, c2} {
		if err := c.Set(ctx, "p1", "v1", WithTags("featured")); err != nil {
			t.Fatal(err)
		}
		if err := c.Set(ctx, "p2", "v2", WithTags("sale")); err != nil {
			t.Fatal(err)
		}
	}

	if err := c1.DeleteByTags(ctx, []string{"featured", "sale"}); err != nil {
		t.Fatal(err)
	}

	// Allow backplane messages to propagate
	time.Sleep(50 * time.Millisecond)

	for _, key := range []string{"p1", "p2"} {
		if _, ok, _ := c2.Get(ctx, key); ok {
			t.Errorf("%s should be evicted from node 2's L1", key)
		}
	}
}
