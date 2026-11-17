package cache_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/coefficient-engineering/cache"
	memorybp "github.com/coefficient-engineering/cache/adapters/backplane/memory"
	memoryl2 "github.com/coefficient-engineering/cache/adapters/l2/memory"
	jsonserializer "github.com/coefficient-engineering/cache/adapters/serializer/json"
	"github.com/coefficient-engineering/cache/backplane"
)

func newRemoteTagNode(t *testing.T, bp backplane.Backplane, id string, opts ...cache.Option) cache.Cache {
	t.Helper()
	options := []cache.Option{cache.WithBackplane(bp), cache.WithNodeID(id), cache.WithCacheName("shared"), cache.WithKeyPrefix("tenant:")}
	c, err := cache.New(append(options, opts...)...)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Error(err)
		}
	})
	return c
}

func observeTagDelivery(t *testing.T, c cache.Cache) <-chan struct{} {
	t.Helper()
	delivered := make(chan struct{}, 1)
	t.Cleanup(c.Events().On(func(event cache.Event) {
		if received, ok := event.(cache.EventBackplaneReceived); ok && received.Type == backplane.MessageTypeDeleteByTags {
			select {
			case delivered <- struct{}{}:
			default:
			}
		}
	}))
	return delivered
}

func awaitRemoteTagDelivery(t *testing.T, delivered <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	select {
	case <-delivered:
	case <-ctx.Done():
		t.Fatal("tag invalidation was not processed")
	}
}

func seedRemoteTagValue(t *testing.T, c cache.Cache, key string, tags ...string) {
	t.Helper()
	if err := c.Set(t.Context(), key, key, cache.WithTags(tags...), func(o *cache.EntryOptions) { o.SkipBackplaneNotifications = true }); err != nil {
		t.Fatal(err)
	}
	if value, found, err := c.Get(t.Context(), key); err != nil || !found || value != key {
		t.Fatalf("seed %s = (%v,%v,%v)", key, value, found, err)
	}
}

func TestRemoteTagInvalidation_PublicCacheLayers(t *testing.T) {
	for _, withL2 := range []bool{false, true} {
		layer := "L1"
		if withL2 {
			layer = "L1+L2"
		}
		for _, test := range []struct {
			name             string
			invalidate       func(context.Context, cache.Cache) error
			missing, present []string
		}{
			{"single", func(ctx context.Context, c cache.Cache) error { return c.DeleteByTag(ctx, "red") }, []string{"red", "both"}, []string{"blue", "other", "untagged"}},
			{"union", func(ctx context.Context, c cache.Cache) error {
				return c.DeleteByTags(ctx, []string{"red", "blue", "red", "blue"})
			}, []string{"red", "blue", "both"}, []string{"other", "untagged"}},
		} {
			t.Run(layer+"/"+test.name, func(t *testing.T) {
				hub := memorybp.NewHub()
				var options []cache.Option
				if withL2 {
					options = []cache.Option{cache.WithL2(memoryl2.New()), cache.WithSerializer(&jsonserializer.Serializer{})}
				}
				a := newRemoteTagNode(t, memorybp.NewWithHub("a", hub), "a", options...)
				b := newRemoteTagNode(t, memorybp.NewWithHub("b", hub), "b", options...)
				seedRemoteTagValue(t, b, "red", "red")
				seedRemoteTagValue(t, b, "blue", "blue")
				seedRemoteTagValue(t, b, "both", "red", "blue", "red")
				seedRemoteTagValue(t, b, "other", "green")
				seedRemoteTagValue(t, b, "untagged")
				if _, found, err := a.Get(t.Context(), "both", func(o *cache.EntryOptions) { o.SkipL2Read = true }); err != nil || found {
					t.Fatalf("sender must have no local entry: found=%v err=%v", found, err)
				}
				delivered := observeTagDelivery(t, b)
				if err := test.invalidate(t.Context(), a); err != nil {
					t.Fatal(err)
				}
				awaitRemoteTagDelivery(t, delivered)
				for _, key := range test.missing {
					if value, found, err := b.Get(t.Context(), key); err != nil || found {
						t.Errorf("invalidated %s = (%v,%v,%v)", key, value, found, err)
					}
				}
				for _, key := range test.present {
					if value, found, err := b.Get(t.Context(), key); err != nil || !found || value != key {
						t.Errorf("unrelated %s = (%v,%v,%v)", key, value, found, err)
					}
				}
			})
		}
	}
}

func TestRemoteTagInvalidation_CacheNamespaceIsolation(t *testing.T) {
	hub := memorybp.NewHub()
	publisher := memorybp.NewWithHub("a", hub)
	a := newRemoteTagNode(t, publisher, "a")
	cases := []struct {
		name, prefix string
		invalidated  bool
	}{
		{"shared", "tenant:", true}, {"shared", "other:", false}, {"other", "tenant:", false}, {"shared", "", false},
	}
	for i, test := range cases {
		t.Run(test.name+"/"+test.prefix, func(t *testing.T) {
			b := newRemoteTagNode(t, memorybp.NewWithHub("b", hub), "b", cache.WithCacheName(test.name), cache.WithKeyPrefix(test.prefix))
			seedRemoteTagValue(t, b, "value", "red")
			delivered := make(chan struct{}, 1)
			marker := "barrier"
			t.Cleanup(b.Events().On(func(event cache.Event) {
				if received, ok := event.(cache.EventBackplaneReceived); ok && received.Key == marker {
					delivered <- struct{}{}
				}
			}))
			if err := a.DeleteByTag(t.Context(), "red", func(o *cache.EntryOptions) { o.AllowBackgroundBackplaneOperations = false }); err != nil {
				t.Fatal(err)
			}
			// Ordered memory delivery gives each namespace its own completion barrier.
			if err := publisher.Publish(t.Context(), backplane.Message{Type: backplane.MessageTypeDelete, CacheName: test.name, KeyPrefix: test.prefix, Key: marker}); err != nil {
				t.Fatal(err)
			}
			awaitRemoteTagDelivery(t, delivered)
			value, found, err := b.Get(t.Context(), "value")
			if err != nil || found == test.invalidated || (!test.invalidated && value != "value") {
				t.Fatalf("namespace case %d = (%v,%v,%v)", i, value, found, err)
			}
		})
	}
}

func TestRemoteTagInvalidation_DiscardsFailSafeCopies(t *testing.T) {
	hub := memorybp.NewHub()
	a := newRemoteTagNode(t, memorybp.NewWithHub("a", hub), "a")
	b := newRemoteTagNode(t, memorybp.NewWithHub("b", hub), "b")
	options := []cache.EntryOption{cache.WithTags("red"), cache.WithFailSafe(time.Hour, time.Second), func(o *cache.EntryOptions) { o.SkipBackplaneNotifications = true }}
	if err := b.Set(t.Context(), "value", "old", options...); err != nil {
		t.Fatal(err)
	}
	delivered := observeTagDelivery(t, b)
	if err := a.DeleteByTag(t.Context(), "red"); err != nil {
		t.Fatal(err)
	}
	awaitRemoteTagDelivery(t, delivered)
	if value, found, err := b.Get(t.Context(), "value", cache.WithAllowStaleOnReadOnly()); err != nil || found {
		t.Fatalf("invalidated fail-safe read = (%v,%v,%v)", value, found, err)
	}
	down := errors.New("factory unavailable")
	value, err := b.GetOrSet(t.Context(), "value", func(context.Context, *cache.FactoryExecutionContext) (any, error) { return nil, down }, options...)
	if !errors.Is(err, down) || value != nil {
		t.Fatalf("invalidated fail-safe factory = (%v,%v)", value, err)
	}
}
