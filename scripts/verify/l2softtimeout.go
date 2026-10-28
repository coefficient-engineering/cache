package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/coefficient-engineering/cache"
)

const (
	elapsedKey     = "elapsed"
	slowGetDelay   = time.Second
	l2SoftTimeout  = 50 * time.Millisecond
	softEntryTTL   = 200 * time.Millisecond
	softFastBound  = slowGetDelay / 2
	staleWaitExtra = 100 * time.Millisecond
)

// slowGetHook delays Redis GET commands while enabled, simulating a slow L2.
type slowGetHook struct{ enabled atomic.Bool }

func (*slowGetHook) DialHook(next redis.DialHook) redis.DialHook { return next }

func (h *slowGetHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		if h.enabled.Load() && cmd.Name() == "get" {
			select {
			case <-time.After(slowGetDelay):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return next(ctx, cmd)
	}
}

func (*slowGetHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

// runL2SoftTimeout proves DistributedCacheSoftTimeout lets GetOrSet skip a
// slow Redis read when a fail-safe stale value exists, and waits otherwise.
func runL2SoftTimeout(ctx context.Context, r *report) error {
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		return errors.New("REDIS_ADDR is not set; see docs/agents/verification/redis-multinode/README.md")
	}
	runID := fmt.Sprintf("verify:%d:", time.Now().UnixNano())
	c, client, err := newRedisL2Cache(addr, runID, cache.WithDefaultEntryOptions(cache.EntryOptions{
		Duration: softEntryTTL, IsFailSafeEnabled: true, FailSafeMaxDuration: time.Hour,
		FailSafeThrottleDuration: time.Second, ReThrowSerializationExceptions: true,
	}))
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	defer func() { _ = c.Close() }()
	defer func() { _ = cleanupRun(ctx, client, runID) }()
	hook := &slowGetHook{}
	client.AddHook(hook)

	for _, k := range []string{"a", "b"} {
		if err := c.Set(ctx, k, "old"); err != nil {
			return err
		}
	}
	time.Sleep(softEntryTTL + staleWaitExtra) // both entries are now stale in L1 and L2
	hook.enabled.Store(true)
	timeouts := cache.WithDistributedCacheTimeouts(l2SoftTimeout, 0)

	start := time.Now()
	v, err := c.GetOrSet(ctx, "a", func(context.Context, *cache.FactoryExecutionContext) (any, error) {
		return nil, errors.New("database down")
	}, timeouts)
	elapsed := time.Since(start)
	r.record(check{
		Name: "soft-timeout-serves-stale-without-waiting-for-l2", OK: err == nil && v == "old" && elapsed < softFastBound,
		Observed: map[string]any{valueKey: v, errorKey: errString(err), elapsedKey: elapsed.String()},
	})

	calls := 0
	start = time.Now()
	v, err = c.GetOrSet(ctx, "b", func(context.Context, *cache.FactoryExecutionContext) (any, error) {
		calls++
		return freshValue, nil
	}, timeouts)
	elapsed = time.Since(start)
	r.record(check{
		Name: "soft-timeout-calls-factory-without-waiting-for-l2", OK: err == nil && v == freshValue && calls == 1 && elapsed < softFastBound,
		Observed: map[string]any{valueKey: v, factoryCallsKey: calls, elapsedKey: elapsed.String()},
	})

	noFailSafe := func(o *cache.EntryOptions) { o.IsFailSafeEnabled = false }
	start = time.Now()
	_, err = c.GetOrSet(ctx, "missing", func(context.Context, *cache.FactoryExecutionContext) (any, error) {
		return freshValue, nil
	}, noFailSafe, timeouts)
	elapsed = time.Since(start)
	r.record(check{
		Name: "no-fallback-waits-for-slow-l2", OK: err == nil && elapsed >= slowGetDelay,
		Observed: map[string]any{errorKey: errString(err), elapsedKey: elapsed.String()},
	})
	hook.enabled.Store(false)
	return nil
}
