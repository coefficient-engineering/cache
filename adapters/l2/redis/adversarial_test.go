package redis

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
)

type (
	commandBoundary struct {
		run func(context.Context, goredis.Cmder, redisNext) error
	}
	redisNext = goredis.ProcessHook
)

func (commandBoundary) DialHook(next goredis.DialHook) goredis.DialHook { return next }
func (commandBoundary) ProcessPipelineHook(next goredis.ProcessPipelineHook) goredis.ProcessPipelineHook {
	return next
}

func (h commandBoundary) ProcessHook(next goredis.ProcessHook) goredis.ProcessHook {
	return func(ctx context.Context, cmd goredis.Cmder) error { return h.run(ctx, cmd, next) }
}

func TestRedisL2_MaintenanceCannotDeleteAConcurrentReplacement(t *testing.T) {
	mr := miniredis.RunT(t)
	admin := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	defer admin.Close()
	replacement, err := New(admin, WithKeyPrefix("race:"))
	if err != nil {
		t.Fatal(err)
	}
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	defer client.Close()
	a, err := New(client, WithKeyPrefix("race:"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	if err := a.Set(ctx, "key", []byte("old"), 0, []string{"retire"}); err != nil {
		t.Fatal(err)
	}
	key := "race:fc:v2:data:" + base64.RawURLEncoding.EncodeToString([]byte("key"))
	observed, err := admin.Get(ctx, key).Result()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.DeleteByTag(ctx, "retire"); err != nil {
		t.Fatal(err)
	}
	completed := make(chan error, 1)
	var replaced atomic.Bool
	client.AddHook(commandBoundary{run: func(ctx context.Context, cmd goredis.Cmder, next redisNext) error {
		args := cmd.Args()
		target := cmd.Name() == "eval" && len(args) == 5 && fmt.Sprint(args[3]) == key && fmt.Sprint(args[4]) == observed
		if target && replaced.CompareAndSwap(false, true) {
			if err := replacement.Set(ctx, "key", []byte("new"), 0, nil); err != nil {
				completed <- err
				return err
			}
			err := next(ctx, cmd)
			completed <- err
			return err
		}
		return next(ctx, cmd)
	}})
	maintenanceCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.RunMaintenance(maintenanceCtx) }()
	select {
	case err := <-completed:
		if err != nil {
			t.Fatal(err)
		}
	case <-maintenanceCtx.Done():
		t.Fatal("cleanup race was not exercised")
	}
	cancel()
	<-done
	if value, err := a.Get(ctx, "key"); err != nil || string(value) != "new" {
		t.Fatalf("replacement = (%q,%v), want new", value, err)
	}
}

func TestRedisL2_GenerationsRemainDistinctAboveLuaIntegerPrecision(t *testing.T) {
	a, mr := setup(t)
	ctx := t.Context()
	if err := a.Set(ctx, "seed", []byte("seed"), 0, nil); err != nil {
		t.Fatal(err)
	}
	mr.HSet("test:fc:v2:control", "s", "9007199254740992")
	if err := a.Set(ctx, "old", []byte("old"), 0, []string{"high"}); err != nil {
		t.Fatal(err)
	}
	if err := a.DeleteByTag(ctx, "high"); err != nil {
		t.Fatal(err)
	}
	if value, err := a.Get(ctx, "old"); err != nil || value != nil {
		t.Fatalf("old generation after increment = (%q,%v)", value, err)
	}
	if err := a.Set(ctx, "new", []byte("new"), 0, []string{"high"}); err != nil {
		t.Fatal(err)
	}
	if value, err := a.Get(ctx, "new"); err != nil || string(value) != "new" {
		t.Fatalf("new generation = (%q,%v)", value, err)
	}
	mr.HSet("test:fc:v2:control", "s", "9223372036854775807")
	if err := a.DeleteByTag(ctx, "high"); err == nil {
		t.Fatal("generation overflow reported success")
	}
}

func TestRedisL2_ConcurrentRecoveryAdoptsOneIncarnation(t *testing.T) {
	a, mr := setup(t)
	ctx := t.Context()
	if err := a.Set(ctx, "before", []byte("before"), 0, nil); err != nil {
		t.Fatal(err)
	}
	mr.Del("test:fc:v2:control")
	var group sync.WaitGroup
	failures := make(chan error, 16)
	for i := range 16 {
		group.Go(func() {
			key := fmt.Sprintf("key-%d", i)
			failures <- a.Set(ctx, key, []byte(key), 0, []string{"shared"})
		})
	}
	group.Wait()
	close(failures)
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	for i := range 16 {
		key := fmt.Sprintf("key-%d", i)
		if value, err := a.Get(ctx, key); err != nil || string(value) != key {
			t.Fatalf("concurrently recovered %s = (%q,%v)", key, value, err)
		}
	}
	if value, err := a.Get(ctx, "before"); err != nil || value != nil {
		t.Fatalf("old incarnation = (%q,%v)", value, err)
	}
}

func TestRedisL2_RetiredMetadataCannotReviveAnExpiredRecord(t *testing.T) {
	a, mr := setup(t)
	ctx := t.Context()
	if err := a.Set(ctx, "old", []byte("old"), time.Second, []string{"retired"}); err != nil {
		t.Fatal(err)
	}
	client, ok := a.client.(*goredis.Client)
	if !ok {
		t.Fatal("setup did not provide a native standalone client")
	}
	key := "test:fc:v2:data:" + base64.RawURLEncoding.EncodeToString([]byte("old"))
	original, err := client.Get(ctx, key).Bytes()
	if err != nil {
		t.Fatal(err)
	}
	mr.FastForward(2 * time.Second)
	maintenanceCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.RunMaintenance(maintenanceCtx) }()
	field := "t:" + base64.RawURLEncoding.EncodeToString([]byte("retired"))
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for mr.HGet("test:fc:v2:control", field) != "" {
		select {
		case <-ticker.C:
		case <-maintenanceCtx.Done():
			t.Fatal("unused tag metadata was not reclaimed")
		}
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if err := a.Set(ctx, "new", []byte("new"), 0, []string{"retired"}); err != nil {
		t.Fatal(err)
	}
	if err := client.Set(ctx, key, original, 0).Err(); err != nil {
		t.Fatal(err)
	}
	if value, err := a.Get(ctx, "old"); err != nil || value != nil {
		t.Fatalf("replayed expired generation = (%q,%v)", value, err)
	}
	if value, err := a.Get(ctx, "new"); err != nil || string(value) != "new" {
		t.Fatalf("new generation = (%q,%v)", value, err)
	}
}
