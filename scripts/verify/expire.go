package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/coefficient-engineering/cache"
)

// runExpireL2 proves Expire reaches a shared Redis L2: another node's next
// GetOrSet calls the factory, with fail-safe the old value survives in
// Redis as a fallback, and without fail-safe the Redis key is deleted.
func runExpireL2(ctx context.Context, r *report) error {
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		return errors.New("REDIS_ADDR is not set; see docs/agents/verification/redis-multinode/README.md")
	}
	runID := fmt.Sprintf("verify:%d:", time.Now().UnixNano())
	failSafe := cache.WithDefaultEntryOptions(cache.EntryOptions{
		Duration: time.Minute, IsFailSafeEnabled: true, FailSafeMaxDuration: time.Hour,
		FailSafeThrottleDuration: time.Second, ReThrowSerializationExceptions: true,
	})
	a, client, err := newRedisL2Cache(addr, runID, failSafe)
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	defer func() { _ = a.Close() }()
	defer func() { _ = cleanupRun(ctx, client, runID) }()
	b, bClient, err := newRedisL2Cache(addr, runID, failSafe)
	if err != nil {
		return err
	}
	defer func() { _ = bClient.Close() }()
	defer func() { _ = b.Close() }()

	nodes := expireNodes{a: a, b: b, client: client, runID: runID}
	if err := stepExpireFailSafe(ctx, r, nodes); err != nil {
		return err
	}
	return stepExpireNoFailSafe(ctx, r, nodes)
}

// expireNodes are two caches sharing one Redis L2, plus a raw client.
type expireNodes struct {
	a, b   cache.Cache
	client *redis.Client
	runID  string
}

// minRemaining is a lower bound on the physical lifetime left after
// expiring a fresh entry written with FailSafeMaxDuration = 1h.
const minRemaining = 50 * time.Minute

// stepExpireFailSafe: Expire rewrites the envelope; the old value stays a
// fallback on another node; the next GetOrSet calls the factory.
func stepExpireFailSafe(ctx context.Context, r *report, n expireNodes) error {
	a, b, client, runID := n.a, n.b, n.client, n.runID
	if err := a.Set(ctx, "fs", "old"); err != nil {
		return err
	}
	ttlBefore := client.TTL(ctx, runID+"fs").Val()
	err := a.Expire(ctx, "fs")
	raw := client.Get(ctx, runID+"fs").Val()
	var env struct {
		LE int64 `json:"le"`
		PE int64 `json:"pe"`
	}
	_ = json.Unmarshal([]byte(raw), &env)
	ttlAfter := client.TTL(ctx, runID+"fs").Val()
	r.record(check{
		Name:     "failsafe-expire-rewrites-redis-envelope",
		OK:       err == nil && env.LE <= time.Now().UnixMilli() && env.PE > time.Now().Add(minRemaining).UnixMilli() && ttlAfter > minRemaining,
		Observed: map[string]any{errorKey: errString(err), "le": env.LE, "pe": env.PE, "ttlBefore": ttlBefore.String(), "ttlAfter": ttlAfter.String()},
	})

	v, err := b.GetOrSet(ctx, "fs", func(context.Context, *cache.FactoryExecutionContext) (any, error) {
		return nil, errors.New("database down")
	})
	r.record(check{Name: "other-node-gets-l2-fail-safe-fallback", OK: err == nil && v == "old", Observed: map[string]any{valueKey: v, errorKey: errString(err)}})

	calls := 0
	factory := func(context.Context, *cache.FactoryExecutionContext) (any, error) {
		calls++
		return freshValue, nil
	}
	if err := a.Expire(ctx, "fs"); err != nil {
		return err
	}
	v, err = a.GetOrSet(ctx, "fs", factory)
	r.record(check{Name: "same-node-getorset-calls-factory-after-expire", OK: err == nil && v == freshValue && calls == 1, Observed: map[string]any{valueKey: v, factoryCallsKey: calls}})
	return nil
}

// stepExpireNoFailSafe: without fail-safe, Expire deletes the Redis key.
func stepExpireNoFailSafe(ctx context.Context, r *report, n expireNodes) error {
	a, b, client, runID := n.a, n.b, n.client, n.runID
	calls := 0
	factory := func(context.Context, *cache.FactoryExecutionContext) (any, error) {
		calls++
		return freshValue, nil
	}

	noFailSafe := func(o *cache.EntryOptions) { o.IsFailSafeEnabled = false }
	if err := a.Set(ctx, "plain", "old", noFailSafe); err != nil {
		return err
	}
	err := a.Expire(ctx, "plain", noFailSafe)
	exists := client.Exists(ctx, runID+"plain").Val()
	r.record(check{Name: "no-failsafe-expire-deletes-redis-key", OK: err == nil && exists == 0, Observed: map[string]any{errorKey: errString(err), "exists": exists}})

	v, err := b.GetOrSet(ctx, "plain", factory, noFailSafe)
	r.record(check{Name: "other-node-getorset-calls-factory-after-expire", OK: err == nil && v == freshValue && calls == 1, Observed: map[string]any{valueKey: v, factoryCallsKey: calls}})
	return nil
}
