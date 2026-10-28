package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/coefficient-engineering/cache"
	redisbp "github.com/coefficient-engineering/cache/adapters/backplane/redis"
	redisl2 "github.com/coefficient-engineering/cache/adapters/l2/redis"
	"github.com/coefficient-engineering/cache/adapters/serializer/json"
)

const (
	propagationTimeout = 3 * time.Second
	pollInterval       = 20 * time.Millisecond
	multiNodeKey       = "sku:1"
)

// node is one cache instance with its own Redis connections and node ID.
type node struct {
	cache  cache.Cache
	client *redis.Client
	events *eventLog
}

func newNode(addr, runID, nodeID string) (*node, error) {
	client := redis.NewClient(&redis.Options{Addr: addr})
	c, err := cache.New(
		cache.WithCacheName("verify"),
		cache.WithNodeID(nodeID),
		cache.WithL2(redisl2.New(client, redisl2.WithKeyPrefix(runID))),
		cache.WithSerializer(&json.Serializer{}),
		cache.WithBackplane(redisbp.New(client, redisbp.WithChannel(runID+"backplane"), redisbp.WithNodeID(nodeID))),
	)
	if err != nil {
		return nil, errors.Join(err, client.Close())
	}
	n := &node{cache: c, client: client, events: &eventLog{}}
	n.events.attach(c)
	return n, nil
}

func (n *node) close() error { return errors.Join(n.cache.Close(), n.client.Close()) }

// runMultiNode proves two nodes share L2 through Redis and invalidate each
// other's L1 through the Redis backplane.
func runMultiNode(ctx context.Context, r *report) error {
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		return errors.New("REDIS_ADDR is not set; see docs/agents/verification/redis-multinode/README.md")
	}
	runID := fmt.Sprintf("verify:%d:", time.Now().UnixNano())
	a, err := newNode(addr, runID, "node-a")
	if err != nil {
		return err
	}
	defer func() { _ = a.close() }()
	b, err := newNode(addr, runID, "node-b")
	if err != nil {
		return err
	}
	defer func() { _ = b.close() }()
	defer func() { _ = cleanupRun(ctx, a.client, runID) }()

	for _, step := range []func(context.Context, *report, *node, *node, string) error{
		stepSharedL2, stepUpdatePropagates, stepDeletePropagates,
	} {
		if err := step(ctx, r, a, b, runID); err != nil {
			return err
		}
	}
	return nil
}

// stepSharedL2: node A writes, Redis holds the key, node B reads it via L2.
func stepSharedL2(ctx context.Context, r *report, a, b *node, runID string) error {
	if err := a.cache.Set(ctx, multiNodeKey, "v1"); err != nil {
		return err
	}
	exists, err := a.client.Exists(ctx, runID+multiNodeKey).Result()
	if err != nil {
		return err
	}
	r.record(check{
		Name: "l2-key-written-to-redis", OK: exists == 1,
		Observed: map[string]any{"redis_key": runID + multiNodeKey, "exists": exists},
	})

	got, ok, err := cache.Get[string](ctx, b.cache, multiNodeKey)
	hits := b.events.count("EventL2Hit")
	r.record(check{
		Name: "node-b-reads-node-a-write-via-l2", OK: err == nil && ok && got == "v1" && hits == 1,
		Observed: map[string]any{valueKey: got, "found": ok, "l2_hits": hits, "error": errString(err)},
	})
	return nil
}

// stepUpdatePropagates: node A overwrites; node B's L1 copy is invalidated.
func stepUpdatePropagates(ctx context.Context, r *report, a, b *node, _ string) error {
	if err := a.cache.Set(ctx, multiNodeKey, "v2"); err != nil {
		return err
	}
	got, ok := waitFor(ctx, b.cache, func(v string, found bool) bool { return found && v == "v2" })
	r.record(check{
		Name: "node-b-sees-update-via-backplane", OK: ok && got == "v2",
		Observed: map[string]any{valueKey: got, "backplane_received": b.events.count("EventBackplaneReceived")},
	})
	return nil
}

// stepDeletePropagates: node A deletes; node B and Redis no longer have it.
func stepDeletePropagates(ctx context.Context, r *report, a, b *node, runID string) error {
	if err := a.cache.Delete(ctx, multiNodeKey); err != nil {
		return err
	}
	_, found := waitFor(ctx, b.cache, func(_ string, found bool) bool { return !found })
	exists, err := a.client.Exists(ctx, runID+multiNodeKey).Result()
	if err != nil {
		return err
	}
	r.record(check{
		Name: "delete-propagates-to-node-b-and-redis", OK: !found && exists == 0,
		Observed: map[string]any{"node_b_found": found, "redis_exists": exists},
	})
	return nil
}

// waitFor polls node b until cond holds or propagationTimeout elapses and
// returns the last observed value.
func waitFor(ctx context.Context, c cache.Cache, cond func(string, bool) bool) (value string, found bool) {
	deadline := time.Now().Add(propagationTimeout)
	for {
		v, ok, err := cache.Get[string](ctx, c, multiNodeKey)
		if err == nil && cond(v, ok) {
			return v, ok
		}
		if time.Now().After(deadline) {
			return v, ok
		}
		time.Sleep(pollInterval)
	}
}

// cleanupRun deletes every Redis key this run created.
func cleanupRun(ctx context.Context, client *redis.Client, runID string) error {
	keys, err := client.Keys(ctx, runID+"*").Result()
	if err != nil || len(keys) == 0 {
		return err
	}
	return client.Del(ctx, keys...).Err()
}
