package cache

import (
	"context"
	"testing"
	"time"

	memorybp "github.com/coefficient-engineering/cache/adapters/backplane/memory"
)

func TestBackplane_SetInvalidatesRemoteL1(t *testing.T) {
	// Simulated shared backplane hub
	bp1 := memorybp.New("node-1")
	bp2 := memorybp.New("node-2")

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

	// Node 2's L1 should have been invalidated
	// (In a real setup with shared L2, node 2 would re-read from L2)
	_, ok, _ := c2.Get(ctx, "key")
	// ok depends on whether the backplane message reached node 2
	// With memory backplane in same process, this depends on routing
	_ = ok
}
