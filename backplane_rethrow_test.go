package cache

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/coefficient-engineering/cache/backplane"
)

var errPublish = errors.New("backplane unreachable")

// failingBackplane rejects every publish.
type failingBackplane struct{}

func (failingBackplane) Publish(context.Context, backplane.Message) error { return errPublish }

func (failingBackplane) Subscribe(backplane.Handler) (func(), error) { return func() {}, nil }

func (failingBackplane) Close() error { return nil }

func syncRethrow(o *EntryOptions) {
	o.AllowBackgroundBackplaneOperations = false
	o.ReThrowBackplaneExceptions = true
}

func TestSet_ReturnsBackplaneErrorWhenSyncAndRethrow(t *testing.T) {
	c, _ := newTestCache(t, WithBackplane(failingBackplane{}))
	ctx := context.Background()

	err := c.Set(ctx, "k", "v", syncRethrow)
	if !errors.Is(err, errPublish) {
		t.Fatalf("Set error = %v, want wrapping %v", err, errPublish)
	}

	// The local write still happened.
	got, ok, err := c.Get(ctx, "k")
	if err != nil || !ok || got != "v" {
		t.Fatalf("Get after failed publish = (%v, %v, %v), want (v, true, nil)", got, ok, err)
	}
}

func TestMutations_ReturnBackplaneErrorWhenSyncAndRethrow(t *testing.T) {
	ctx := context.Background()
	cases := map[string]func(Cache) error{
		"Delete":      func(c Cache) error { return c.Delete(ctx, "k", syncRethrow) },
		"Expire":      func(c Cache) error { return c.Expire(ctx, "k", syncRethrow) },
		"DeleteByTag": func(c Cache) error { return c.DeleteByTag(ctx, "t", syncRethrow) },
		// Clear has no per-call options; it follows the cache defaults.
		"Clear": func(c Cache) error { return c.Clear(ctx, false) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			defaults := EntryOptions{Duration: time.Minute}
			syncRethrow(&defaults)
			c, _ := newTestCache(t, WithBackplane(failingBackplane{}), WithDefaultEntryOptions(defaults))
			// Seed a tagged entry; ignore its own publish error.
			_ = c.Set(ctx, "k", "v", WithTags("t"))

			if err := mutate(c); !errors.Is(err, errPublish) {
				t.Fatalf("%s error = %v, want wrapping %v", name, err, errPublish)
			}
		})
	}
}

func TestMutations_SwallowBackplaneErrorUnlessSyncAndRethrow(t *testing.T) {
	ctx := context.Background()
	cases := map[string]EntryOption{
		"background (default)": func(*EntryOptions) {},
		"sync without rethrow": func(o *EntryOptions) { o.AllowBackgroundBackplaneOperations = false },
		"background with rethrow": func(o *EntryOptions) {
			o.ReThrowBackplaneExceptions = true
		},
	}
	for name, opt := range cases {
		t.Run(name, func(t *testing.T) {
			c, _ := newTestCache(t, WithBackplane(failingBackplane{}))
			if err := c.Set(ctx, "k", "v", opt); err != nil {
				t.Fatalf("Set error = %v, want nil", err)
			}
			if err := c.Delete(ctx, "k", opt); err != nil {
				t.Fatalf("Delete error = %v, want nil", err)
			}
		})
	}
}
