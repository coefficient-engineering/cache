package redis

import (
	"context"
	"encoding/base64"
	"errors"
	"testing"
	"time"
)

func TestRedisL2_MaintenanceReclaimsUnreadInfiniteRecords(t *testing.T) {
	a, mr := setup(t)
	ctx := t.Context()
	if err := a.Set(ctx, "retired", []byte("old"), 0, []string{"retire"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Set(ctx, "live", []byte("live"), 0, []string{"keep"}); err != nil {
		t.Fatal(err)
	}
	if err := a.DeleteByTag(ctx, "retire"); err != nil {
		t.Fatal(err)
	}
	physical := "test:fc:v2:data:" + base64.RawURLEncoding.EncodeToString([]byte("retired"))
	if !mr.Exists(physical) {
		t.Fatal("logical invalidation unexpectedly removed the physical record")
	}
	maintenanceCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.RunMaintenance(maintenanceCtx) }()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for mr.Exists(physical) {
		select {
		case <-maintenanceCtx.Done():
			t.Fatal("maintenance did not reclaim the unread TTL-zero record")
		case <-ticker.C:
		}
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("maintenance cancellation = %v", err)
	}
	if value, err := a.Get(ctx, "live"); err != nil || string(value) != "live" {
		t.Fatalf("unrelated record = (%q, %v), want live", value, err)
	}
}
