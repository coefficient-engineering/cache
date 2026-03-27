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
	c1.Set(ctx, "key", "v1")
	c2.Set(ctx, "key", "v1")

	// Node 1 updates the key
	c1.Set(ctx, "key", "v2")

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
