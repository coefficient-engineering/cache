package redis

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
)

type suppliedPrimaries struct{ client *goredis.Client }

func (p *suppliedPrimaries) PrimaryForKey(context.Context, string) (*goredis.Client, error) {
	return p.client, nil
}

func (p *suppliedPrimaries) ForEachPrimary(ctx context.Context, fn func(context.Context, *goredis.Client) error) error {
	return fn(ctx, p.client)
}

type wrappedCommands struct{ goredis.Cmdable }

func TestRedisL2_RejectsUnsupportedCapabilitiesBeforeUse(t *testing.T) {
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	defer client.Close()
	var absentClient *goredis.Client
	var absentAccess *suppliedPrimaries
	cases := []struct {
		name    string
		client  goredis.Cmdable
		options []Option
	}{
		{"nil client", nil, nil},
		{"typed nil client", absentClient, nil},
		{"unknown wrapper", &wrappedCommands{client}, nil},
		{"typed nil access", client, []Option{WithPrimaryAccess(absentAccess)}},
		{"invalid interval", client, []Option{WithMaintenanceInterval(0)}},
		{"invalid batch", client, []Option{WithMaintenanceBatchSize(0)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if adapter, err := New(tc.client, tc.options...); err == nil || adapter != nil {
				t.Fatalf("New = (%v,%v), want construction error", adapter, err)
			}
		})
	}
}

func TestRedisL2_ExplicitWrapperCapabilitiesPreserveBehavior(t *testing.T) {
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	defer client.Close()
	a, err := New(&wrappedCommands{client}, WithPrimaryAccess(&suppliedPrimaries{client: client}))
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	if err := a.Set(ctx, "key", []byte("value"), 0, []string{"tag"}); err != nil {
		t.Fatal(err)
	}
	if value, err := a.Get(ctx, "key"); err != nil || string(value) != "value" {
		t.Fatalf("Get = (%q,%v)", value, err)
	}
	if err := a.DeleteByTag(ctx, "tag"); err != nil {
		t.Fatal(err)
	}
	if value, err := a.Get(ctx, "key"); err != nil || value != nil {
		t.Fatalf("Get invalidated = (%q,%v)", value, err)
	}
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatalf("adapter closed caller client: %v", err)
	}
}
