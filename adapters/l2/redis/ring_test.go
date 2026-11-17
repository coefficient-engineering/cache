package redis

import (
	"context"
	"encoding/base64"
	"fmt"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
)

func TestRedisL2_MaintenanceReclaimsOriginalRingShardAfterRemap(t *testing.T) {
	original := miniredis.RunT(t)
	added := miniredis.RunT(t)
	metadata := miniredis.RunT(t)
	control := goredis.NewClient(&goredis.Options{Addr: metadata.Addr()})
	defer control.Close()
	ring := goredis.NewRing(&goredis.RingOptions{Addrs: map[string]string{"original": original.Addr()}})
	defer ring.Close()
	a, err := New(ring, WithKeyPrefix("ring:"), WithControlClient(control), WithMaintenanceInterval(time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	for i := range 64 {
		key := fmt.Sprintf("key-%d", i)
		if err := a.Set(ctx, key, []byte("old"), 0, []string{"retired"}); err != nil {
			t.Fatal(err)
		}
	}
	ring.SetAddrs(map[string]string{"original": original.Addr(), "added": added.Addr()})
	var orphan string
	for i := range 64 {
		key := fmt.Sprintf("key-%d", i)
		physical := "ring:fc:v2:data:" + base64.RawURLEncoding.EncodeToString([]byte(key))
		primary, err := ring.GetShardClientForKey(physical)
		if err != nil {
			t.Fatal(err)
		}
		if primary.Options().Addr == added.Addr() {
			orphan = physical
			break
		}
	}
	if orphan == "" || !original.Exists(orphan) {
		t.Fatal("fixture did not retain an old record whose route changed")
	}
	if err := a.DeleteByTag(ctx, "retired"); err != nil {
		t.Fatal(err)
	}
	maintenanceCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.RunMaintenance(maintenanceCtx) }()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for original.Exists(orphan) {
		select {
		case <-ticker.C:
		case <-maintenanceCtx.Done():
			t.Fatal("invalidated TTL-zero bytes survived on the original Ring shard")
		}
	}
	cancel()
	<-done
	if err := a.Set(ctx, "fresh", []byte("fresh"), 0, []string{"retired"}); err != nil {
		t.Fatal(err)
	}
	if value, err := a.Get(ctx, "fresh"); err != nil || string(value) != "fresh" {
		t.Fatalf("fresh Ring payload = (%q,%v)", value, err)
	}
}
