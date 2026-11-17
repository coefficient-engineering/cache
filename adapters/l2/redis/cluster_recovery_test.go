//go:build integration

package redis_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"

	adapterredis "github.com/coefficient-engineering/cache/adapters/l2/redis"
)

func cleanRedisClusterNamespace(t *testing.T, client *goredis.ClusterClient, prefix string) {
	t.Helper()
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := client.ForEachMaster(ctx, func(ctx context.Context, primary *goredis.Client) error {
			var cursor uint64
			for {
				keys, next, err := primary.Scan(ctx, cursor, prefix+"fc:v2:*", 128).Result()
				if err != nil {
					return err
				}
				for _, key := range keys {
					if err := primary.Unlink(ctx, key).Err(); err != nil {
						return err
					}
				}
				cursor = next
				if cursor == 0 {
					return nil
				}
			}
		}); err != nil {
			t.Errorf("clean Cluster namespace: %v", err)
		}
	})
}

func clusterRecoveryAdapter(t *testing.T, client *goredis.ClusterClient, prefix string, options ...adapterredis.Option) *adapterredis.Adapter {
	t.Helper()
	options = append(options, adapterredis.WithKeyPrefix(prefix))
	a, err := adapterredis.New(client, options...)
	if err != nil {
		t.Fatalf("construct native Cluster adapter: %v", err)
	}
	return a
}

func assertClusterPayload(t *testing.T, ctx context.Context, a *adapterredis.Adapter, key string, want []byte) {
	t.Helper()
	got, err := a.Get(ctx, key)
	if err != nil || string(got) != string(want) || (got == nil) != (want == nil) {
		t.Fatalf("Get(%q) = (%q, %v), want %q", key, got, err, want)
	}
}

func TestRedisClusterConcurrentControlRecovery(t *testing.T) {
	client, _ := newRedisClusterClient(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	prefix := "integration:" + rand.Text() + ":"
	cleanRedisClusterNamespace(t, client, prefix)
	const writers = 8
	adapters := make([]*adapterredis.Adapter, writers)
	for i := range adapters {
		adapters[i] = clusterRecoveryAdapter(t, client, prefix)
	}
	previous := make([]string, 0, writers*3)
	for round := range 3 {
		if err := client.Del(ctx, prefix+"fc:v2:control").Err(); err != nil {
			t.Fatalf("remove control hash in round %d: %v", round, err)
		}
		start := make(chan struct{})
		results := make(chan error, writers)
		keys := make([]string, writers)
		for writer, a := range adapters {
			keys[writer] = fmt.Sprintf("round:%d:writer:%d", round, writer)
			go func(a *adapterredis.Adapter, key string) {
				<-start
				results <- a.Set(ctx, key, []byte(key), 0, []string{"shared"})
			}(a, keys[writer])
		}
		close(start)
		var writeErr error
		for range writers {
			if err := <-results; err != nil && writeErr == nil {
				writeErr = err
			}
		}
		if writeErr != nil {
			t.Fatalf("concurrent recovery in round %d: %v", round, writeErr)
		}
		for _, key := range previous {
			assertClusterPayload(t, ctx, adapters[0], key, nil)
		}
		for _, key := range keys {
			assertClusterPayload(t, ctx, adapters[0], key, []byte(key))
		}
		previous = append(previous, keys...)
	}
	if ttl, err := client.TTL(ctx, prefix+"fc:v2:control").Result(); err != nil || ttl != -1 {
		t.Fatalf("recovered control TTL = (%s, %v), want persistent", ttl, err)
	}
}

// Hooks delay ordinary Redis commands at the exported client's transport seam;
// neither test inspects private adapter state or substitutes a fake server.
type clusterCommandHook func(goredis.ProcessHook) goredis.ProcessHook

func (clusterCommandHook) DialHook(next goredis.DialHook) goredis.DialHook {
	return next
}

func (h clusterCommandHook) ProcessHook(next goredis.ProcessHook) goredis.ProcessHook {
	return h(next)
}

func (clusterCommandHook) ProcessPipelineHook(next goredis.ProcessPipelineHook) goredis.ProcessPipelineHook {
	return next
}

func TestRedisClusterLateWireWriteCannotReviveInvalidatedData(t *testing.T) {
	client, _ := newRedisClusterClient(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	prefix := "integration:" + rand.Text() + ":"
	cleanRedisClusterNamespace(t, client, prefix)
	a := clusterRecoveryAdapter(t, client, prefix)
	witness := clusterRecoveryAdapter(t, client, prefix)
	wireKey := prefix + "fc:v2:data:" + base64.RawURLEncoding.EncodeToString([]byte("late"))
	var armed atomic.Bool
	armed.Store(true)
	blocked := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	client.AddHook(clusterCommandHook(func(next goredis.ProcessHook) goredis.ProcessHook {
		return func(ctx context.Context, cmd goredis.Cmder) error {
			args := cmd.Args()
			if cmd.Name() == "set" && len(args) > 1 && args[1] == wireKey && armed.CompareAndSwap(true, false) {
				close(blocked)
				select {
				case <-release:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return next(ctx, cmd)
		}
	}))
	result := make(chan error, 1)
	go func() { result <- a.Set(ctx, "late", []byte("old-wire"), 0, []string{"shared"}) }()
	select {
	case <-blocked:
	case <-ctx.Done():
		t.Fatal("payload write did not reach the fault barrier")
	}
	if err := witness.DeleteByTag(ctx, "shared"); err != nil {
		t.Fatalf("invalidate while payload write is delayed: %v", err)
	}
	unblock()
	if err := <-result; err != nil {
		t.Fatalf("complete delayed payload write: %v", err)
	}
	assertClusterPayload(t, ctx, witness, "late", nil)

	// Replaying those exact bytes after complete metadata loss must still miss.
	wire, err := client.Get(ctx, wireKey).Bytes()
	if err != nil {
		t.Fatalf("capture delayed wire record: %v", err)
	}
	if err := client.Del(ctx, prefix+"fc:v2:control").Err(); err != nil {
		t.Fatal(err)
	}
	if err := witness.Set(ctx, "fresh", []byte("fresh"), 0, []string{"shared"}); err != nil {
		t.Fatalf("recover after metadata loss: %v", err)
	}
	if err := client.Set(ctx, wireKey, wire, 0).Err(); err != nil {
		t.Fatalf("replay original wire record: %v", err)
	}
	assertClusterPayload(t, ctx, witness, "late", nil)
	assertClusterPayload(t, ctx, witness, "fresh", []byte("fresh"))
}

func TestRedisClusterMaintenanceProtectsConcurrentOverwrite(t *testing.T) {
	client, _ := newRedisClusterClient(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	prefix := "integration:" + rand.Text() + ":"
	cleanRedisClusterNamespace(t, client, prefix)
	a := clusterRecoveryAdapter(t, client, prefix, adapterredis.WithMaintenanceInterval(time.Hour))
	if err := a.Set(ctx, "overwrite", []byte("stale"), 0, []string{"old"}); err != nil {
		t.Fatal(err)
	}
	if err := a.DeleteByTag(ctx, "old"); err != nil {
		t.Fatal(err)
	}
	wireKey := prefix + "fc:v2:data:" + base64.RawURLEncoding.EncodeToString([]byte("overwrite"))
	blocked := make(chan struct{})
	release := make(chan struct{})
	deleted := make(chan error, 1)
	var armed atomic.Bool
	armed.Store(true)
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	defer unblock()
	client.AddHook(clusterCommandHook(func(next goredis.ProcessHook) goredis.ProcessHook {
		return func(ctx context.Context, cmd goredis.Cmder) error {
			args := cmd.Args()
			if cmd.Name() == "eval" && len(args) > 3 && args[3] == wireKey && strings.Contains(fmt.Sprint(args[1]), "UNLINK") && armed.CompareAndSwap(true, false) {
				close(blocked)
				select {
				case <-release:
				case <-ctx.Done():
					return ctx.Err()
				}
				err := next(ctx, cmd)
				deleted <- err
				return err
			}
			return next(ctx, cmd)
		}
	}))
	maintenanceCtx, stop := context.WithCancel(ctx)
	defer stop()
	finished := make(chan error, 1)
	go func() { finished <- a.RunMaintenance(maintenanceCtx) }()
	select {
	case <-blocked:
	case <-ctx.Done():
		t.Fatal("maintenance did not reach conditional reclamation")
	}
	// Untagged replacement avoids the separately allowed conservative retirement
	// miss: this assertion isolates the wire-record compare-before-UNLINK race.
	if err := a.Set(ctx, "overwrite", []byte("replacement"), 0, nil); err != nil {
		t.Fatalf("overwrite during maintenance: %v", err)
	}
	unblock()
	select {
	case err := <-deleted:
		if err != nil {
			t.Fatalf("conditional reclamation: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("maintenance did not complete conditional reclamation")
	}
	stop()
	if err := <-finished; !errors.Is(err, context.Canceled) {
		t.Fatalf("RunMaintenance after cancellation = %v", err)
	}
	assertClusterPayload(t, ctx, a, "overwrite", []byte("replacement"))
	if count, err := client.Exists(ctx, wireKey).Result(); err != nil || count != 1 {
		t.Fatalf("replacement durable presence = (%d, %v), want 1", count, err)
	}
}

func TestRedisClusterControlRecoveryDuringSlotMigration(t *testing.T) {
	client, _ := newRedisClusterClient(t, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	// Choose an empty slot so this test can restore its original ownership
	// without moving or deleting another test's keys.
	var prefix, controlKey string
	var slot int64
	var source *goredis.Client
	for range 64 {
		prefix = "integration:" + rand.Text() + ":"
		controlKey = prefix + "fc:v2:control"
		var err error
		slot, err = client.ClusterKeySlot(ctx, controlKey).Result()
		if err != nil {
			t.Fatal(err)
		}
		source, err = client.MasterForKey(ctx, controlKey)
		if err != nil {
			t.Fatal(err)
		}
		keys, err := source.Do(ctx, "CLUSTER", "GETKEYSINSLOT", slot, 1).Slice()
		if err != nil {
			t.Fatal(err)
		}
		if len(keys) == 0 {
			break
		}
		source = nil
	}
	if source == nil {
		t.Fatal("could not select an empty control slot")
	}
	var primaries []*goredis.Client
	var primariesMu sync.Mutex
	if err := client.ForEachMaster(ctx, func(_ context.Context, primary *goredis.Client) error {
		primariesMu.Lock()
		primaries = append(primaries, primary)
		primariesMu.Unlock()
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var target *goredis.Client
	for _, primary := range primaries {
		if primary.Options().Addr != source.Options().Addr {
			target = primary
			break
		}
	}
	if target == nil {
		t.Fatal("could not select a different importing primary")
	}
	sourceID, err := source.Do(ctx, "CLUSTER", "MYID").Text()
	if err != nil {
		t.Fatal(err)
	}
	targetID, err := target.Do(ctx, "CLUSTER", "MYID").Text()
	if err != nil {
		t.Fatal(err)
	}
	// Topology changes can synchronously persist Cluster configuration.
	// Keep their administrative I/O budget separate from cache-call defaults.
	const administrationTimeout = 15 * time.Second
	source = source.WithTimeout(administrationTimeout)
	target = target.WithTimeout(administrationTimeout)
	for i, primary := range primaries {
		primaries[i] = primary.WithTimeout(administrationTimeout)
	}
	cleanRedisClusterNamespace(t, client, prefix)
	// Registered after namespace cleanup: restore topology before its SCAN pass.
	// This also runs on failures partway through MIGRATING/IMPORTING setup.
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		for _, primary := range primaries {
			// ASKING permits removal from the importing node before ownership
			// moves. MOVED on nonowners is expected; all possible owners run it.
			_, err := primary.Pipelined(cleanupCtx, func(pipe goredis.Pipeliner) error {
				pipe.Do(cleanupCtx, "ASKING")
				pipe.Del(cleanupCtx, controlKey)
				return nil
			})
			if err != nil && !strings.HasPrefix(err.Error(), "MOVED ") && !strings.HasPrefix(err.Error(), "ASK ") {
				t.Errorf("remove migration control key on %s: %v", primary.Options().Addr, err)
			}
		}
		for _, primary := range primaries {
			if err := primary.Do(cleanupCtx, "CLUSTER", "SETSLOT", slot, "STABLE").Err(); err != nil {
				t.Errorf("clear slot migration on %s: %v", primary.Options().Addr, err)
			}
		}
		for _, primary := range primaries {
			if err := primary.Do(cleanupCtx, "CLUSTER", "SETSLOT", slot, "NODE", sourceID).Err(); err != nil {
				t.Errorf("restore slot ownership on %s: %v", primary.Options().Addr, err)
			}
		}
		client.ReloadState(cleanupCtx)
	})
	a := clusterRecoveryAdapter(t, client, prefix)
	// Keep all payloads out of the migrating slot; this regression isolates
	// control initialization rather than accidental multi-key migration.
	keys := make([]string, 0, 3)
	for candidate := 0; len(keys) < 3; candidate++ {
		key := fmt.Sprintf("payload:%d", candidate)
		wireKey := prefix + "fc:v2:data:" + base64.RawURLEncoding.EncodeToString([]byte(key))
		payloadSlot, err := client.ClusterKeySlot(ctx, wireKey).Result()
		if err != nil {
			t.Fatal(err)
		}
		if payloadSlot != slot {
			keys = append(keys, key)
		}
	}
	if err := a.Set(ctx, keys[0], []byte("before-loss"), 0, []string{"shared"}); err != nil {
		t.Fatal(err)
	}
	if err := source.Del(ctx, controlKey).Err(); err != nil {
		t.Fatal(err)
	}
	if err := target.Do(ctx, "CLUSTER", "SETSLOT", slot, "IMPORTING", sourceID).Err(); err != nil {
		t.Fatalf("set importing slot: %v", err)
	}
	if err := source.Do(ctx, "CLUSTER", "SETSLOT", slot, "MIGRATING", targetID).Err(); err != nil {
		t.Fatalf("set migrating slot: %v", err)
	}
	// Direct primary commands do not hide redirections. This proves Redis
	// actually emits ASK for the missing root before public adapter recovery.
	err = source.Eval(ctx, "return redis.call('EXISTS', KEYS[1])", []string{controlKey}).Err()
	if err == nil || !strings.HasPrefix(err.Error(), fmt.Sprintf("ASK %d ", slot)) {
		t.Fatalf("missing control root on migrating source = %v, want ASK for slot %d", err, slot)
	}
	if err := a.Set(ctx, keys[1], []byte("during-ask"), 0, []string{"shared"}); err != nil {
		t.Fatalf("initialize control root through ASK: %v", err)
	}
	assertClusterPayload(t, ctx, a, keys[0], nil)
	assertClusterPayload(t, ctx, a, keys[1], []byte("during-ask"))
	var root *goredis.MapStringStringCmd
	_, err = target.Pipelined(ctx, func(pipe goredis.Pipeliner) error {
		pipe.Do(ctx, "ASKING")
		root = pipe.HGetAll(ctx, controlKey)
		return nil
	})
	if err != nil {
		t.Fatalf("read initialized root on importing target: %v", err)
	}
	fields, err := root.Result()
	if err != nil || len(fields["i"]) != 32 || fields["s"] == "" || fields["c"] == "" {
		t.Fatalf("root created on importing target = (%v, %v)", fields, err)
	}
	// Finalize actual ownership, leaving the same native Cluster client alive.
	assigned := make(map[string]bool)
	for _, primary := range append([]*goredis.Client{target, source}, primaries...) {
		address := primary.Options().Addr
		if assigned[address] {
			continue
		}
		assigned[address] = true
		if err := primary.Do(ctx, "CLUSTER", "SETSLOT", slot, "NODE", targetID).Err(); err != nil {
			t.Fatalf("transfer slot ownership to target: %v", err)
		}
	}
	err = source.Eval(ctx, "return redis.call('EXISTS', KEYS[1])", []string{controlKey}).Err()
	if err == nil || !strings.HasPrefix(err.Error(), fmt.Sprintf("MOVED %d ", slot)) {
		t.Fatalf("former control owner = %v, want MOVED for slot %d", err, slot)
	}
	if err := target.Del(ctx, controlKey).Err(); err != nil {
		t.Fatalf("lose control root after ownership transfer: %v", err)
	}
	if err := a.Set(ctx, keys[2], []byte("after-moved"), 0, []string{"shared"}); err != nil {
		t.Fatalf("recover control root after MOVED ownership: %v", err)
	}
	assertClusterPayload(t, ctx, a, keys[0], nil)
	assertClusterPayload(t, ctx, a, keys[1], nil)
	assertClusterPayload(t, ctx, a, keys[2], []byte("after-moved"))
}
