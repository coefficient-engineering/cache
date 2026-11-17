package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/redis/go-redis/v9"

	redisl2 "github.com/coefficient-engineering/cache/adapters/l2/redis"
)

const (
	clusterPrimaryCount = 3
	clusterTimeout      = 30 * time.Second
	clusterCleanupTime  = 5 * time.Second
	clusterPollTime     = 20 * time.Millisecond
	clusterRemaining    = "remaining"
	clusterRecoveryTag  = "recovery"
	clusterAfterLoss    = "after-loss"
)

type clusterTagEntry struct {
	key  string
	tags []string
}

type clusterTagRun struct {
	report       *report
	a, b         *redisl2.Adapter
	client       *redis.ClusterClient
	prefix       string
	keys         []string
	entries      []clusterTagEntry
	initialCount int
	payload      []byte
}

// runClusterTags drives exported adapter operations on a real Cluster.
func runClusterTags(ctx context.Context, r *report) (result error) {
	options, err := clusterTagOptions()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, clusterTimeout)
	defer cancel()
	aClient := redis.NewClusterClient(options)
	defer func() { result = errors.Join(result, aClient.Close()) }()
	bClient := redis.NewClusterClient(options)
	defer func() { result = errors.Join(result, bClient.Close()) }()
	prefix := fmt.Sprintf("verify:cluster-tags:%d:", time.Now().UnixNano())
	a, err := redisl2.New(aClient, redisl2.WithKeyPrefix(prefix))
	if err != nil {
		return err
	}
	b, err := redisl2.New(bClient, redisl2.WithKeyPrefix(prefix), redisl2.WithMaintenanceInterval(clusterPollTime))
	if err != nil {
		return err
	}
	keys, err := clusterTagKeys(ctx, aClient, prefix)
	if err != nil {
		return err
	}
	s := &clusterTagRun{
		report: r, a: a, b: b, client: aClient, prefix: prefix, keys: keys,
		payload: []byte{0, 255, 'v', '2', '\n', 0},
	}
	s.prepareEntries()
	defer func() { result = errors.Join(result, s.cleanup(ctx)) }()
	r.record(check{Name: "payloads-span-three-primaries", OK: len(keys) == clusterPrimaryCount, Observed: keys})
	for _, step := range []func(context.Context) error{s.seed, s.invalidate, s.recover, s.reclaim} {
		if err := step(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (s *clusterTagRun) prepareEntries() {
	s.entries = []clusterTagEntry{
		{s.keys[0], []string{tagRed, tagBlue, tagRed}},
		{s.keys[1], []string{tagRed}},
		{s.keys[2], []string{tagBlue, tagGreen}},
		{clusterRemaining, []string{tagGreen}},
		{"unrelated", []string{"purple"}},
		{"untagged", nil},
	}
	s.initialCount = len(s.entries)
	s.entries = append(s.entries,
		clusterTagEntry{"before-loss-tagged", []string{clusterRecoveryTag}},
		clusterTagEntry{"before-loss-untagged", nil})
}

func (s *clusterTagRun) cleanup(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), clusterCleanupTime)
	defer cancel()
	var result error
	for _, entry := range s.entries {
		result = errors.Join(result, s.a.Delete(ctx, entry.key))
	}
	return errors.Join(result, s.a.Delete(ctx, clusterAfterLoss), s.client.Del(ctx, s.prefix+"fc:v2:control").Err())
}

func (s *clusterTagRun) read(ctx context.Context, name, key string, expected []byte) {
	value, err := s.b.Get(ctx, key)
	matches := bytes.Equal(value, expected) && (expected != nil || value == nil)
	s.report.record(check{
		Name: name, OK: err == nil && matches,
		Observed: map[string]any{
			entryKey: key, "present": value != nil, "payload": value,
			"expected_present": expected != nil, errorKey: errString(err),
		},
	})
}

func (s *clusterTagRun) seed(ctx context.Context) error {
	for _, entry := range s.entries[:s.initialCount] {
		if err := s.a.Set(ctx, entry.key, s.payload, 0, entry.tags); err != nil {
			return err
		}
		s.read(ctx, "other-client-reads-opaque-payload", entry.key, s.payload)
	}
	return nil
}

func (s *clusterTagRun) invalidate(ctx context.Context) error {
	if err := s.b.DeleteByTags(ctx, []string{tagRed, tagBlue, tagRed, tagBlue}); err != nil {
		return err
	}
	for _, key := range s.keys {
		s.read(ctx, "overlapping-duplicate-tags-invalidated", key, nil)
	}
	for _, key := range []string{clusterRemaining, "unrelated", "untagged"} {
		s.read(ctx, "bulk-delete-preserves-other-entries", key, s.payload)
	}
	if err := s.a.DeleteByTag(ctx, tagGreen); err != nil {
		return err
	}
	s.read(ctx, "single-tag-invalidates-remaining-match", clusterRemaining, nil)
	s.read(ctx, "single-tag-preserves-unrelated", "unrelated", s.payload)
	s.read(ctx, "single-tag-preserves-untagged", "untagged", s.payload)
	if err := s.b.Clear(ctx); err != nil {
		return err
	}
	s.read(ctx, "clear-invalidates-tagged", "unrelated", nil)
	s.read(ctx, "clear-invalidates-untagged", "untagged", nil)
	return nil
}

func (s *clusterTagRun) recover(ctx context.Context) error {
	for _, entry := range s.entries[s.initialCount:] {
		if err := s.a.Set(ctx, entry.key, s.payload, 0, entry.tags); err != nil {
			return err
		}
		s.read(ctx, "before-metadata-loss-readable", entry.key, s.payload)
	}
	deleted, err := s.client.Del(ctx, s.prefix+"fc:v2:control").Result()
	if err != nil {
		return err
	}
	s.report.record(check{Name: "metadata-hash-loss-injected", OK: deleted == 1, Observed: deleted})
	for _, entry := range s.entries[s.initialCount:] {
		s.read(ctx, "missing-metadata-never-revives-old-data", entry.key, nil)
	}
	if err := s.a.Set(ctx, clusterAfterLoss, s.payload, 0, []string{clusterRecoveryTag}); err != nil {
		return err
	}
	s.read(ctx, "fresh-write-automatically-recovers-metadata", clusterAfterLoss, s.payload)
	for _, entry := range s.entries {
		s.read(ctx, "recovered-metadata-keeps-old-data-invalid", entry.key, nil)
	}
	return nil
}

func (s *clusterTagRun) reclaim(ctx context.Context) error {
	staleKeys := make([]string, 0, len(s.entries))
	for _, entry := range s.entries {
		key := clusterTagWireKey(s.prefix, entry.key)
		staleKeys = append(staleKeys, key)
		ttl, err := s.client.PTTL(ctx, key).Result()
		if err != nil {
			return err
		}
		s.report.record(check{
			Name: "invalidated-ttl-zero-bytes-await-maintenance", OK: ttl == -1,
			Observed: map[string]any{entryKey: entry.key, "pttl": int64(ttl)},
		})
	}
	maintenanceCtx, stop := context.WithCancel(ctx)
	done := make(chan error, 1)
	go func() { done <- s.b.RunMaintenance(maintenanceCtx) }()
	defer func() {
		stop()
		err := <-done
		s.report.record(check{Name: "application-cancels-maintenance-loop", OK: errors.Is(err, context.Canceled), Observed: errString(err)})
	}()
	waitCtx, cancel := context.WithTimeout(ctx, clusterCleanupTime)
	defer cancel()
	remaining, err := clusterTagWaitReclaimed(waitCtx, s.client, staleKeys)
	s.report.record(check{
		Name: "maintenance-physically-reclaims-ttl-zero-data", OK: err == nil && remaining == 0,
		Observed: map[string]any{"remaining": remaining, "reclaimed": len(staleKeys) - remaining, errorKey: errString(err)},
	})
	s.read(ctx, "maintenance-preserves-fresh-write", clusterAfterLoss, s.payload)
	return nil
}

func clusterTagOptions() (*redis.ClusterOptions, error) {
	raw := strings.TrimSpace(os.Getenv("REDIS_CLUSTER_ADDRS"))
	if raw == "" {
		return nil, errors.New("REDIS_CLUSTER_ADDRS is not set; see docs/agents/verification/redis-multinode/features/cluster-tags.md")
	}
	addrs := strings.Split(raw, ",")
	for i := range addrs {
		addrs[i] = strings.TrimSpace(addrs[i])
		if addrs[i] == "" {
			return nil, errors.New("REDIS_CLUSTER_ADDRS contains an empty address")
		}
	}
	var dialMap map[string]string
	if rawMap := os.Getenv("REDIS_CLUSTER_DIAL_MAP"); rawMap != "" {
		if err := json.Unmarshal([]byte(rawMap), &dialMap); err != nil {
			return nil, fmt.Errorf("parse REDIS_CLUSTER_DIAL_MAP JSON address map: %w", err)
		}
	}
	return &redis.ClusterOptions{
		Addrs:         addrs,
		ReadOnly:      true,
		RouteRandomly: true,
		Dialer: func(ctx context.Context, network, address string) (net.Conn, error) {
			if mapped, ok := dialMap[address]; ok {
				address = mapped
			}
			return (&net.Dialer{Timeout: clusterCleanupTime}).DialContext(ctx, network, address)
		},
	}, nil
}

// The documented v2 layout is used only for topology and physical-byte checks.
func clusterTagWireKey(prefix, key string) string {
	return prefix + "fc:v2:data:" + base64.RawURLEncoding.EncodeToString([]byte(key))
}

func clusterTagKeys(ctx context.Context, client *redis.ClusterClient, prefix string) ([]string, error) {
	var primaries atomic.Int32
	err := client.ForEachMaster(ctx, func(ctx context.Context, primary *redis.Client) error {
		if err := primary.Ping(ctx).Err(); err != nil {
			return err
		}
		primaries.Add(1)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if primaries.Load() != clusterPrimaryCount {
		return nil, fmt.Errorf("cluster-tags needs three primaries, got %d", primaries.Load())
	}
	selected := make(map[string]bool)
	keys := make([]string, 0, clusterPrimaryCount)
	for i := 0; i < 1024 && len(keys) < clusterPrimaryCount; i++ {
		key := fmt.Sprintf("overlap:%d", i)
		primary, err := client.MasterForKey(ctx, clusterTagWireKey(prefix, key))
		if err != nil {
			return nil, err
		}
		address := primary.Options().Addr
		if !selected[address] {
			selected[address] = true
			keys = append(keys, key)
		}
	}
	if len(keys) != clusterPrimaryCount {
		return nil, errors.New("could not select payload keys spanning three primaries")
	}
	return keys, nil
}

func clusterTagWaitReclaimed(ctx context.Context, client *redis.ClusterClient, keys []string) (int, error) {
	ticker := time.NewTicker(clusterPollTime)
	defer ticker.Stop()
	for {
		remaining := 0
		for _, key := range keys {
			n, err := client.Exists(ctx, key).Result()
			if err != nil {
				return len(keys), err
			}
			remaining += int(n)
		}
		if remaining == 0 {
			return 0, nil
		}
		select {
		case <-ctx.Done():
			return remaining, ctx.Err()
		case <-ticker.C:
		}
	}
}
