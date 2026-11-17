//go:build integration

package redis_test

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	goredis "github.com/redis/go-redis/v9"

	adapterredis "github.com/coefficient-engineering/cache/adapters/l2/redis"
)

func newRedisClusterClient(t *testing.T, configure func(*goredis.ClusterOptions)) (*goredis.ClusterClient, []goredis.ClusterSlot) {
	t.Helper()
	addresses := strings.TrimSpace(os.Getenv("REDIS_CLUSTER_ADDRS"))
	if addresses == "" {
		t.Fatal("REDIS_CLUSTER_ADDRS must name a real three-primary Redis Cluster; use mise run test:redis-cluster")
	}
	var dialMap map[string]string
	if raw := os.Getenv("REDIS_CLUSTER_DIAL_MAP"); raw != "" {
		if err := json.Unmarshal([]byte(raw), &dialMap); err != nil {
			t.Fatalf("REDIS_CLUSTER_DIAL_MAP must be a JSON object mapping advertised addresses to dial addresses: %v", err)
		}
	}
	addrs := strings.Split(addresses, ",")
	for i := range addrs {
		addrs[i] = strings.TrimSpace(addrs[i])
		if addrs[i] == "" {
			t.Fatal("REDIS_CLUSTER_ADDRS contains an empty address")
		}
	}
	options := &goredis.ClusterOptions{
		Addrs: addrs,
		Dialer: func(ctx context.Context, network, address string) (net.Conn, error) {
			if mapped, ok := dialMap[address]; ok {
				address = mapped
			}
			return (&net.Dialer{Timeout: 5 * time.Second}).DialContext(ctx, network, address)
		},
	}
	if configure != nil {
		configure(options)
	}
	client := goredis.NewClusterClient(options)
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Errorf("close Cluster client: %v", err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	slots := redisClusterSlots(t, ctx, client)
	primaries := make(map[string]bool)
	for _, slot := range slots {
		if len(slot.Nodes) == 0 {
			t.Fatal("Cluster slot range has no primary")
		}
		primaries[slot.Nodes[0].Addr] = true
	}
	if len(primaries) != 3 {
		t.Fatalf("need three real primaries, got %d", len(primaries))
	}
	return client, slots
}

func redisClusterSlots(t *testing.T, ctx context.Context, client *goredis.ClusterClient) []goredis.ClusterSlot {
	t.Helper()
	shards, err := client.ClusterShards(ctx).Result()
	if err != nil {
		t.Fatalf("read Cluster topology: %v", err)
	}
	var slots []goredis.ClusterSlot
	for _, shard := range shards {
		var primary goredis.ClusterNode
		for _, node := range shard.Nodes {
			if node.Role == "master" {
				primary = goredis.ClusterNode{ID: node.ID, Addr: net.JoinHostPort(node.Endpoint, strconv.FormatInt(node.Port, 10))}
			}
		}
		if primary.ID == "" {
			t.Fatal("Cluster shard has no primary")
		}
		for _, slot := range shard.Slots {
			slots = append(slots, goredis.ClusterSlot{Start: int(slot.Start), End: int(slot.End), Nodes: []goredis.ClusterNode{primary}})
		}
	}
	return slots
}

func TestRedisClusterTagInvalidation(t *testing.T) {
	client, slots := newRedisClusterClient(t, func(options *goredis.ClusterOptions) {
		options.ReadOnly = true
		options.RouteRandomly = true
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	prefix := "integration:" + rand.Text() + ":"
	a, err := adapterredis.New(client, adapterredis.WithKeyPrefix(prefix))
	if err != nil {
		t.Fatalf("construct native Cluster adapter: %v", err)
	}
	// The documented v2 wire key is used only to prove that these public Set
	// calls exercise all three primaries, rather than an accidental same-slot case.
	var distributedKeys []string
	selected := make(map[string]bool)
	for i := 0; i < 1024 && len(selected) < 3; i++ {
		key := fmt.Sprintf("overlap:%d", i)
		wireKey := prefix + "fc:v2:data:" + base64.RawURLEncoding.EncodeToString([]byte(key))
		slotNumber, err := client.ClusterKeySlot(ctx, wireKey).Result()
		if err != nil {
			t.Fatalf("resolve payload hash slot: %v", err)
		}
		for _, slot := range slots {
			if slotNumber >= int64(slot.Start) && slotNumber <= int64(slot.End) && !selected[slot.Nodes[0].Addr] {
				selected[slot.Nodes[0].Addr] = true
				distributedKeys = append(distributedKeys, key)
			}
		}
	}
	if len(distributedKeys) != 3 {
		t.Fatal("could not select payload keys spanning all three primaries")
	}
	entries := []struct {
		key  string
		tags []string
	}{
		{distributedKeys[0], []string{"red", "blue"}},
		{distributedKeys[1], []string{"red"}},
		{distributedKeys[2], []string{"blue", "green"}},
		{"remaining", []string{"green"}},
		{"unrelated", []string{"purple"}},
		{"untagged", nil},
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cleanupCancel()
		for _, entry := range entries {
			if err := a.Delete(cleanupCtx, entry.key); err != nil {
				t.Errorf("clean up %q: %v", entry.key, err)
			}
		}
	})
	assertValue := func(key string, present bool) {
		t.Helper()
		got, err := a.Get(ctx, key)
		if err != nil {
			t.Fatalf("Get(%q): %v", key, err)
		}
		if !present {
			if got != nil {
				t.Fatalf("Get(%q) = %q, want miss", key, got)
			}
		} else if string(got) != "payload:"+key {
			t.Fatalf("Get(%q) = %q, want original opaque payload", key, got)
		}
	}
	for _, entry := range entries {
		if err := a.Set(ctx, entry.key, []byte("payload:"+entry.key), 0, entry.tags); err != nil {
			t.Fatalf("Set(%q) with indefinite TTL: %v", entry.key, err)
		}
		assertValue(entry.key, true)
	}
	if err := a.DeleteByTags(ctx, []string{"red", "blue", "red", "blue"}); err != nil {
		t.Fatalf("bulk invalidation with duplicate overlapping tags: %v", err)
	}
	for _, key := range distributedKeys {
		assertValue(key, false)
	}
	assertValue("remaining", true)
	assertValue("unrelated", true)
	assertValue("untagged", true)

	if err := a.DeleteByTag(ctx, "green"); err != nil {
		t.Fatalf("single-tag invalidation after overlapping bulk invalidation: %v", err)
	}
	for _, key := range distributedKeys {
		assertValue(key, false)
	}
	assertValue("remaining", false)
	assertValue("unrelated", true)
	assertValue("untagged", true)
}
