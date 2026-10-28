package main

import (
	"context"
	"errors"
	"os"
	"strings"

	"github.com/redis/go-redis/v9"

	"github.com/coefficient-engineering/cache"
	redisbp "github.com/coefficient-engineering/cache/adapters/backplane/redis"
)

// syncRethrow makes backplane publishes synchronous and rethrown.
func syncRethrow(o *cache.EntryOptions) {
	o.AllowBackgroundBackplaneOperations = false
	o.ReThrowBackplaneExceptions = true
}

// runBackplaneRethrow proves ReThrowBackplaneExceptions surfaces a real Redis
// publish failure from mutating calls, and that defaults keep swallowing it.
// The backplane's Redis connection is closed after subscribing, so every
// publish fails inside go-redis.
func runBackplaneRethrow(ctx context.Context, r *report) error {
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		return errors.New("REDIS_ADDR is not set; see docs/agents/verification/redis-multinode/README.md")
	}
	client := redis.NewClient(&redis.Options{Addr: addr})
	c, err := cache.New(
		cache.WithNodeID("node-rethrow"),
		cache.WithBackplane(redisbp.New(client, redisbp.WithChannel("verify:rethrow:backplane"))),
	)
	if err != nil {
		return errors.Join(err, client.Close())
	}
	defer func() { _ = c.Close() }()
	if err := client.Close(); err != nil {
		return err
	}

	err = c.Set(ctx, "k", "default")
	r.record(check{Name: "default-set-swallows-publish-failure", OK: err == nil, Observed: errString(err)})

	err = c.Set(ctx, "k", "rethrown", syncRethrow)
	r.record(check{
		Name:     "sync-rethrow-set-returns-publish-error",
		OK:       errors.Is(err, redis.ErrClosed) && strings.Contains(errString(err), "backplane publish"),
		Observed: errString(err),
	})

	got, ok, getErr := cache.Get[string](ctx, c, "k")
	r.record(check{
		Name: "local-write-kept-after-failed-publish", OK: getErr == nil && ok && got == "rethrown",
		Observed: map[string]any{valueKey: got, foundKey: ok},
	})

	err = c.Delete(ctx, "k", syncRethrow)
	r.record(check{Name: "sync-rethrow-delete-returns-publish-error", OK: errors.Is(err, redis.ErrClosed), Observed: errString(err)})
	return nil
}
