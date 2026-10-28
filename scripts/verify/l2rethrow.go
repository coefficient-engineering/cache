package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/coefficient-engineering/cache"
	redisl2 "github.com/coefficient-engineering/cache/adapters/l2/redis"
	jsonserializer "github.com/coefficient-engineering/cache/adapters/serializer/json"
)

// freshValue is what the scenario's factories return.
const freshValue = "fresh"

// factoryCallsKey labels the observed factory call count.
const factoryCallsKey = "factoryCalls"

// rethrowL2 returns L2 adapter errors to the caller.
func rethrowL2(o *cache.EntryOptions) { o.ReThrowDistributedCacheExceptions = true }

// keepL2Quiet opts a call out of L2 error propagation.
func keepL2Quiet(o *cache.EntryOptions) { o.ReThrowDistributedCacheExceptions = false }

// noSerializationRethrow swallows serialization errors.
func noSerializationRethrow(o *cache.EntryOptions) { o.ReThrowSerializationExceptions = false }

// lockedBuffer is a goroutine-safe log sink.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// runL2Rethrow proves L2 adapter and serialization errors reach callers
// exactly as ReThrowDistributedCacheExceptions, SkipL2OnError and
// ReThrowSerializationExceptions say, against a real Redis L2.
func runL2Rethrow(ctx context.Context, r *report) error {
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		return errors.New("REDIS_ADDR is not set; see docs/agents/verification/redis-multinode/README.md")
	}
	runID := fmt.Sprintf("verify:%d:", time.Now().UnixNano())
	if err := stepBrokenL2(ctx, r, addr, runID); err != nil {
		return err
	}
	return stepSerialization(ctx, r, addr, runID)
}

// newRedisL2Cache builds a cache over a Redis L2 at addr.
func newRedisL2Cache(addr, runID string, opts ...cache.Option) (cache.Cache, *redis.Client, error) {
	client := redis.NewClient(&redis.Options{Addr: addr})
	opts = append([]cache.Option{
		cache.WithCacheName("verify"),
		cache.WithL2(redisl2.New(client, redisl2.WithKeyPrefix(runID))),
		cache.WithSerializer(&jsonserializer.Serializer{}),
	}, opts...)
	c, err := cache.New(opts...)
	if err != nil {
		return nil, nil, errors.Join(err, client.Close())
	}
	return c, client, nil
}

// stepBrokenL2 closes the L2's Redis client so every L2 call fails inside
// go-redis, then checks which calls return the failure.
func stepBrokenL2(ctx context.Context, r *report, addr, runID string) error {
	c, client, err := newRedisL2Cache(addr, runID)
	if err != nil {
		return err
	}
	defer func() { _ = c.Close() }()
	events := &eventLog{}
	events.attach(c)
	if err := client.Close(); err != nil {
		return err
	}

	err = c.Set(ctx, "k", "default")
	r.record(check{Name: "default-set-swallows-l2-failure", OK: err == nil, Observed: errString(err)})
	got, ok, err := cache.Get[string](ctx, c, "k")
	r.record(check{
		Name: "local-write-kept-after-failed-l2-write", OK: err == nil && ok && got == "default",
		Observed: map[string]any{valueKey: got, foundKey: ok},
	})
	r.record(check{Name: "l2-error-event-emitted", OK: events.count("EventL2Error") == 1, Observed: events.count("EventL2Error")})

	_, ok, err = c.Get(ctx, "missing")
	r.record(check{Name: "default-get-is-clean-miss", OK: err == nil && !ok, Observed: errString(err)})

	err = c.Set(ctx, "k", "rethrown", rethrowL2)
	r.record(check{
		Name:     "rethrow-set-returns-l2-error",
		OK:       errors.Is(err, redis.ErrClosed) && strings.Contains(errString(err), "L2 write"),
		Observed: errString(err),
	})

	_, _, err = c.Get(ctx, "missing", rethrowL2)
	r.record(check{Name: "rethrow-get-returns-l2-error", OK: errors.Is(err, redis.ErrClosed), Observed: errString(err)})

	calls := 0
	_, err = c.GetOrSet(ctx, "missing", func(context.Context, *cache.FactoryExecutionContext) (any, error) {
		calls++
		return freshValue, nil
	}, rethrowL2)
	r.record(check{
		Name: "rethrow-getorset-returns-l2-read-error-without-factory", OK: errors.Is(err, redis.ErrClosed) && calls == 0,
		Observed: map[string]any{errorKey: errString(err), factoryCallsKey: calls},
	})

	err = c.Set(ctx, "k", "background", rethrowL2, cache.WithBackgroundL2Ops())
	r.record(check{Name: "rethrow-background-set-swallows-l2-error", OK: err == nil, Observed: errString(err)})

	for name, mutate := range map[string]func() error{
		"delete":        func() error { return c.Delete(ctx, "k", rethrowL2) },
		"delete-by-tag": func() error { return c.DeleteByTag(ctx, "t", rethrowL2) },
	} {
		err := mutate()
		r.record(check{Name: "rethrow-" + name + "-returns-l2-error", OK: errors.Is(err, redis.ErrClosed), Observed: errString(err)})
	}
	return stepSkipL2OnError(ctx, r, addr, runID)
}

// stepSkipL2OnError checks that SkipL2OnError=false makes a broken L2's
// errors propagate by default and that a call can opt back out.
func stepSkipL2OnError(ctx context.Context, r *report, addr, runID string) error {
	strict, strictClient, err := newRedisL2Cache(addr, runID, func(o *cache.Options) { o.SkipL2OnError = false })
	if err != nil {
		return err
	}
	defer func() { _ = strict.Close() }()
	if err := strictClient.Close(); err != nil {
		return err
	}
	err = strict.Set(ctx, "k", "v")
	r.record(check{Name: "skipl2onerror-false-set-returns-l2-error", OK: errors.Is(err, redis.ErrClosed), Observed: errString(err)})
	err = strict.Clear(ctx, true)
	r.record(check{Name: "skipl2onerror-false-clear-returns-l2-error", OK: errors.Is(err, redis.ErrClosed), Observed: errString(err)})
	err = strict.Set(ctx, "k", "v", keepL2Quiet)
	r.record(check{Name: "skipl2onerror-false-per-call-opt-out", OK: err == nil, Observed: errString(err)})
	return nil
}

// stepSerialization uses a live Redis L2 to check serialization failures
// on write (an unencodable value) and read (a corrupt stored envelope).
func stepSerialization(ctx context.Context, r *report, addr, runID string) error {
	logs := &lockedBuffer{}
	c, client, err := newRedisL2Cache(addr, runID, cache.WithLogger(slog.New(slog.NewTextHandler(logs, nil))))
	if err != nil {
		return err
	}
	defer func() { _ = client.Close() }()
	defer func() { _ = c.Close() }()
	defer func() { _ = cleanupRun(ctx, client, runID) }()

	err = c.Set(ctx, "chan", make(chan int))
	var unsupported *json.UnsupportedTypeError
	r.record(check{Name: "default-set-returns-serialization-error", OK: errors.As(err, &unsupported), Observed: errString(err)})

	err = c.Set(ctx, "chan", make(chan int), noSerializationRethrow)
	r.record(check{
		Name: "no-rethrow-set-logs-serialization-error", OK: err == nil && strings.Count(logs.String(), "L2 serialization failed") == 2,
		Observed: map[string]any{errorKey: errString(err), "logLines": strings.Count(logs.String(), "L2 serialization failed")},
	})

	if err := client.Set(ctx, runID+"bad", "not an envelope", time.Minute).Err(); err != nil {
		return err
	}
	_, ok, err := c.Get(ctx, "bad")
	var syntaxErr *json.SyntaxError
	r.record(check{Name: "default-get-returns-deserialization-error", OK: !ok && errors.As(err, &syntaxErr), Observed: errString(err)})

	calls := 0
	factory := func(context.Context, *cache.FactoryExecutionContext) (any, error) {
		calls++
		return freshValue, nil
	}
	_, err = c.GetOrSet(ctx, "bad", factory)
	r.record(check{
		Name: "default-getorset-returns-deserialization-error-without-factory", OK: errors.As(err, &syntaxErr) && calls == 0,
		Observed: map[string]any{errorKey: errString(err), factoryCallsKey: calls},
	})

	v, err := c.GetOrSet(ctx, "bad", factory, noSerializationRethrow)
	stored, redisErr := client.Get(ctx, runID+"bad").Result()
	r.record(check{
		Name: "no-rethrow-getorset-treats-corrupt-entry-as-miss",
		OK:   err == nil && v == freshValue && calls == 1 && redisErr == nil && strings.Contains(stored, `"le"`),
		Observed: map[string]any{
			valueKey: v, errorKey: errString(err), factoryCallsKey: calls, "redisValue": stored,
		},
	})
	return nil
}
