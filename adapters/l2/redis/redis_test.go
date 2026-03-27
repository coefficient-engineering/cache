package redis

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
)

func setup(t *testing.T) (*Adapter, *miniredis.Miniredis) {
	t.Helper()
	mr := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { client.Close() })
	adapter := New(client, WithKeyPrefix("test:"))
	return adapter, mr
}

func TestRedisL2_GetSet(t *testing.T) {
	a, _ := setup(t)
	ctx := context.Background()

	err := a.Set(ctx, "hello", []byte("world"), 5*time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}

	got, err := a.Get(ctx, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "world" {
		t.Fatalf("got %q, want %q", got, "world")
	}
}

func TestRedisL2_GetMiss(t *testing.T) {
	a, _ := setup(t)
	ctx := context.Background()

	got, err := a.Get(ctx, "nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("expected nil on miss, got %v", got)
	}
}

func TestRedisL2_TTLExpiry(t *testing.T) {
	a, mr := setup(t)
	ctx := context.Background()

	err := a.Set(ctx, "ephemeral", []byte("data"), 10*time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}

	got, err := a.Get(ctx, "ephemeral")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("expected data, got nil")
	}

	mr.FastForward(11 * time.Second)

	got, err = a.Get(ctx, "ephemeral")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("expected nil after expiry, got %v", got)
	}
}

func TestRedisL2_Delete(t *testing.T) {
	a, _ := setup(t)
	ctx := context.Background()

	if err := a.Delete(ctx, "ghost"); err != nil {
		t.Fatal(err)
	}

	if err := a.Set(ctx, "k1", []byte("v1"), 5*time.Minute, nil); err != nil {
		t.Fatal(err)
	}
	if err := a.Delete(ctx, "k1"); err != nil {
		t.Fatal(err)
	}
	got, err := a.Get(ctx, "k1")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("expected nil after delete, got %v", got)
	}
}

func TestRedisL2_DeleteWithTags(t *testing.T) {
	a, _ := setup(t)
	ctx := context.Background()

	if err := a.Set(ctx, "k1", []byte("v1"), 5*time.Minute, []string{"t1", "t2"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Delete(ctx, "k1"); err != nil {
		t.Fatal(err)
	}

	if err := a.DeleteByTag(ctx, "t1"); err != nil {
		t.Fatal(err)
	}
}

func TestRedisL2_DeleteByTag(t *testing.T) {
	a, _ := setup(t)
	ctx := context.Background()

	if err := a.Set(ctx, "p1", []byte("product1"), 5*time.Minute, []string{"featured"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Set(ctx, "p2", []byte("product2"), 5*time.Minute, []string{"featured", "sale"}); err != nil {
		t.Fatal(err)
	}
	if err := a.Set(ctx, "p3", []byte("product3"), 5*time.Minute, []string{"sale"}); err != nil {
		t.Fatal(err)
	}

	if err := a.DeleteByTag(ctx, "featured"); err != nil {
		t.Fatal(err)
	}

	for _, key := range []string{"p1", "p2"} {
		got, err := a.Get(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		if got != nil {
			t.Fatalf("expected %s to be deleted, got %v", key, got)
		}
	}

	got, err := a.Get(ctx, "p3")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("expected p3 to still exist")
	}
}

func TestRedisL2_DeleteByTag_NonExistent(t *testing.T) {
	a, _ := setup(t)
	ctx := context.Background()

	if err := a.DeleteByTag(ctx, "ghost-tag"); err != nil {
		t.Fatal(err)
	}
}

func TestRedisL2_Clear(t *testing.T) {
	a, _ := setup(t)
	ctx := context.Background()

	if err := a.Set(ctx, "a", []byte("1"), 5*time.Minute, nil); err != nil {
		t.Fatal(err)
	}
	if err := a.Set(ctx, "b", []byte("2"), 5*time.Minute, nil); err != nil {
		t.Fatal(err)
	}

	client := a.client.(*goredis.Client)
	client.Set(ctx, "other:key", "foreign", 5*time.Minute)

	if err := a.Clear(ctx); err != nil {
		t.Fatal(err)
	}

	for _, key := range []string{"a", "b"} {
		got, err := a.Get(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		if got != nil {
			t.Fatalf("expected %s to be cleared, got %v", key, got)
		}
	}

	val, err := client.Get(ctx, "other:key").Result()
	if err != nil {
		t.Fatal(err)
	}
	if val != "foreign" {
		t.Fatalf("expected foreign key to survive, got %q", val)
	}
}

func TestRedisL2_Ping(t *testing.T) {
	a, _ := setup(t)
	if err := a.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestRedisL2_TagCleanupOnOverwrite(t *testing.T) {
	a, _ := setup(t)
	ctx := context.Background()

	if err := a.Set(ctx, "k", []byte("v1"), 5*time.Minute, []string{"a", "b"}); err != nil {
		t.Fatal(err)
	}

	if err := a.Set(ctx, "k", []byte("v2"), 5*time.Minute, []string{"c"}); err != nil {
		t.Fatal(err)
	}

	if err := a.DeleteByTag(ctx, "a"); err != nil {
		t.Fatal(err)
	}

	got, err := a.Get(ctx, "k")
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("expected key to survive after deleting old tag")
	}
	if string(got) != "v2" {
		t.Fatalf("got %q, want %q", got, "v2")
	}

	if err := a.DeleteByTag(ctx, "c"); err != nil {
		t.Fatal(err)
	}

	got, err = a.Get(ctx, "k")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("expected key to be deleted by new tag, got %v", got)
	}
}
