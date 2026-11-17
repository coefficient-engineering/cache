package redis

import (
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/alicebob/miniredis/v2/server"
	goredis "github.com/redis/go-redis/v9"
	"github.com/redis/go-redis/v9/maintnotifications"
)

func TestRedisL2_RecoveryWithEnabledMaintenanceNotifications(t *testing.T) {
	mr := miniredis.RunT(t)
	// Model a server accepting notification negotiation. The SDK's actual
	// protected-handler registry and connection initialization are unchanged.
	mr.Server().SetPreHook(func(peer *server.Peer, cmd string, args ...string) bool {
		if strings.EqualFold(cmd, "client") && len(args) > 0 && strings.EqualFold(args[0], "maint_notifications") {
			peer.WriteOK()
			return true
		}
		return false
	})
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr(), Protocol: 3, MaintNotificationsConfig: &maintnotifications.Config{Mode: maintnotifications.ModeEnabled}})
	defer client.Close()
	a, err := New(client, WithKeyPrefix("notifications:"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	if err := a.Set(ctx, "old", []byte("old"), 0, nil); err != nil {
		t.Fatal(err)
	}
	mr.Del("notifications:fc:v2:control")
	if err := a.Set(ctx, "new", []byte("new"), 0, nil); err != nil {
		t.Fatal(err)
	}
	if value, err := a.Get(ctx, "old"); err != nil || value != nil {
		t.Fatalf("old incarnation = (%q,%v), want miss", value, err)
	}
	if value, err := a.Get(ctx, "new"); err != nil || string(value) != "new" {
		t.Fatalf("recovered payload = (%q,%v), want new", value, err)
	}
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatalf("caller client no longer usable: %v", err)
	}
}
