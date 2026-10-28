package cache

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	memoryl2 "github.com/coefficient-engineering/cache/adapters/l2/memory"
	jsonserializer "github.com/coefficient-engineering/cache/adapters/serializer/json"
)

var errL2 = errors.New("l2 unreachable")

// failingL2 rejects every L2 operation.
type failingL2 struct{}

func (failingL2) Get(context.Context, string) ([]byte, error) { return nil, errL2 }

func (failingL2) Set(context.Context, string, []byte, time.Duration, []string) error { return errL2 }

func (failingL2) Delete(context.Context, string) error { return errL2 }

func (failingL2) DeleteByTag(context.Context, string) error { return errL2 }

func (failingL2) Clear(context.Context) error { return errL2 }

func (failingL2) Ping(context.Context) error { return errL2 }

// syncBuffer is a goroutine-safe log sink.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func rethrowL2(o *EntryOptions) { o.ReThrowDistributedCacheExceptions = true }

func noSerializationRethrow(o *EntryOptions) { o.ReThrowSerializationExceptions = false }

func newFailingL2Cache(t *testing.T, opts ...Option) Cache {
	t.Helper()
	opts = append([]Option{WithL2(failingL2{}), WithSerializer(&jsonserializer.Serializer{})}, opts...)
	c, _ := newTestCache(t, opts...)
	return c
}

func TestL2Mutations_ReturnL2ErrorWhenSyncAndRethrow(t *testing.T) {
	ctx := context.Background()
	cases := map[string]func(Cache) error{
		"Set":         func(c Cache) error { return c.Set(ctx, "k", "v2", rethrowL2) },
		"Delete":      func(c Cache) error { return c.Delete(ctx, "k", rethrowL2) },
		"DeleteByTag": func(c Cache) error { return c.DeleteByTag(ctx, "t", rethrowL2) },
		// Clear has no per-call options; it follows the cache defaults.
		"Clear": func(c Cache) error { return c.Clear(ctx, true) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			defaults := EntryOptions{Duration: time.Minute}
			rethrowL2(&defaults)
			c := newFailingL2Cache(t, WithDefaultEntryOptions(defaults))
			// Seed a tagged L1 entry; its own L2 write fails.
			_ = c.Set(ctx, "k", "v", WithTags("t"))

			if err := mutate(c); !errors.Is(err, errL2) {
				t.Fatalf("%s error = %v, want wrapping %v", name, err, errL2)
			}
		})
	}
}

func TestL2Mutations_ApplyLocalChangeDespiteRethrownL2Error(t *testing.T) {
	ctx := context.Background()
	c := newFailingL2Cache(t)

	if err := c.Set(ctx, "k", "v", rethrowL2); !errors.Is(err, errL2) {
		t.Fatalf("Set error = %v, want wrapping %v", err, errL2)
	}
	got, ok, err := c.Get(ctx, "k")
	if err != nil || !ok || got != "v" {
		t.Fatalf("Get after failed L2 write = (%v, %v, %v), want (v, true, nil)", got, ok, err)
	}

	if err := c.Delete(ctx, "k", rethrowL2); !errors.Is(err, errL2) {
		t.Fatalf("Delete error = %v, want wrapping %v", err, errL2)
	}
	if _, ok, _ := c.Get(ctx, "k", WithSkipL2()); ok {
		t.Fatal("L1 entry survived a Delete whose L2 delete failed")
	}
}

func TestL2Mutations_SwallowL2ErrorUnlessRethrow(t *testing.T) {
	ctx := context.Background()
	c := newFailingL2Cache(t)
	var l2Errors int
	var mu sync.Mutex
	c.Events().On(func(e Event) {
		if _, ok := e.(EventL2Error); ok {
			mu.Lock()
			l2Errors++
			mu.Unlock()
		}
	})

	if err := c.Set(ctx, "k", "v", WithTags("t")); err != nil {
		t.Fatalf("Set error = %v, want nil", err)
	}
	if err := c.Delete(ctx, "k"); err != nil {
		t.Fatalf("Delete error = %v, want nil", err)
	}
	if err := c.DeleteByTag(ctx, "t"); err != nil {
		t.Fatalf("DeleteByTag error = %v, want nil", err)
	}
	if err := c.Clear(ctx, true); err != nil {
		t.Fatalf("Clear error = %v, want nil", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if l2Errors != 4 {
		t.Fatalf("EventL2Error count = %d, want 4", l2Errors)
	}
}

func TestSet_BackgroundL2WriteNeverReturnsL2Error(t *testing.T) {
	c := newFailingL2Cache(t)
	if err := c.Set(context.Background(), "k", "v", rethrowL2, WithBackgroundL2Ops()); err != nil {
		t.Fatalf("Set error = %v, want nil for a background L2 write", err)
	}
}

func TestSkipL2OnErrorFalse_RethrowsL2ErrorsByDefault(t *testing.T) {
	ctx := context.Background()
	c := newFailingL2Cache(t, func(o *Options) { o.SkipL2OnError = false })

	if !c.DefaultEntryOptions().ReThrowDistributedCacheExceptions {
		t.Fatal("SkipL2OnError=false did not enable ReThrowDistributedCacheExceptions in the defaults")
	}
	if err := c.Set(ctx, "k", "v"); !errors.Is(err, errL2) {
		t.Fatalf("Set error = %v, want wrapping %v", err, errL2)
	}
	optOut := func(o *EntryOptions) { o.ReThrowDistributedCacheExceptions = false }
	if err := c.Set(ctx, "k", "v", optOut); err != nil {
		t.Fatalf("Set with per-entry opt-out error = %v, want nil", err)
	}
}

func TestReads_ReturnL2ErrorWhenRethrow(t *testing.T) {
	ctx := context.Background()
	c := newFailingL2Cache(t)

	if _, ok, err := c.Get(ctx, "k", rethrowL2); ok || !errors.Is(err, errL2) {
		t.Fatalf("Get = (ok %v, err %v), want (false, wrapping %v)", ok, err, errL2)
	}

	factory, calls := factoryCounter("v")
	if _, err := c.GetOrSet(ctx, "k", factory, rethrowL2); !errors.Is(err, errL2) {
		t.Fatalf("GetOrSet error = %v, want wrapping %v", err, errL2)
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("factory called %d times after a rethrown L2 read error, want 0", n)
	}
}

func TestReads_SwallowL2ErrorWithoutRethrow(t *testing.T) {
	ctx := context.Background()
	c := newFailingL2Cache(t)

	if v, ok, err := c.Get(ctx, "k"); v != nil || ok || err != nil {
		t.Fatalf("Get = (%v, %v, %v), want a clean miss", v, ok, err)
	}
	factory, calls := factoryCounter("v")
	if v, err := c.GetOrSet(ctx, "k", factory); err != nil || v != "v" {
		t.Fatalf("GetOrSet = (%v, %v), want (v, nil)", v, err)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("factory called %d times, want 1", n)
	}
}

func newSerializationCache(t *testing.T, logs *syncBuffer) (Cache, *memoryl2.Adapter) {
	t.Helper()
	l2 := memoryl2.New()
	c, _ := newTestCache(t,
		WithL2(l2),
		WithSerializer(&jsonserializer.Serializer{}),
		WithLogger(slog.New(slog.NewTextHandler(logs, nil))),
	)
	return c, l2
}

func TestSet_ReturnsSerializationErrorByDefault(t *testing.T) {
	var logs syncBuffer
	c, _ := newSerializationCache(t, &logs)
	ctx := context.Background()

	err := c.Set(ctx, "k", make(chan int))
	var unsupported *json.UnsupportedTypeError
	if !errors.As(err, &unsupported) {
		t.Fatalf("Set error = %v, want a *json.UnsupportedTypeError", err)
	}
	if _, ok, _ := c.Get(ctx, "k", WithSkipL2()); !ok {
		t.Fatal("L1 write was not kept after an L2 serialization failure")
	}
}

func TestSerializationWriteFailure_LoggedWhenNotReturned(t *testing.T) {
	ctx := context.Background()
	cases := map[string][]EntryOption{
		"rethrow off":         {noSerializationRethrow},
		"background L2 write": {WithBackgroundL2Ops()},
	}
	for name, opts := range cases {
		t.Run(name, func(t *testing.T) {
			var logs syncBuffer
			c, _ := newSerializationCache(t, &logs)
			if err := c.Set(ctx, "k", make(chan int), opts...); err != nil {
				t.Fatalf("Set error = %v, want nil", err)
			}
			deadline := time.Now().Add(time.Second)
			for !strings.Contains(logs.String(), "serialization failed") {
				if time.Now().After(deadline) {
					t.Fatalf("serialization failure not logged; logs: %q", logs.String())
				}
				time.Sleep(time.Millisecond)
			}
		})
	}
}

func TestGetOrSet_KeepsFactoryValueWhenL2WriteFails(t *testing.T) {
	var logs syncBuffer
	c, _ := newSerializationCache(t, &logs)
	ch := make(chan int)
	v, err := c.GetOrSet(context.Background(), "k", func(context.Context, *FactoryExecutionContext) (any, error) {
		return ch, nil
	}, rethrowL2)
	if err != nil || v != ch {
		t.Fatalf("GetOrSet = (%v, %v), want (factory value, nil)", v, err)
	}
	if !strings.Contains(logs.String(), "serialization failed") {
		t.Fatalf("serialization failure not logged; logs: %q", logs.String())
	}
}

func TestReads_ReturnDeserializationErrorByDefault(t *testing.T) {
	var logs syncBuffer
	c, l2 := newSerializationCache(t, &logs)
	ctx := context.Background()
	if err := l2.Set(ctx, "k", []byte("not an envelope"), 0, nil); err != nil {
		t.Fatal(err)
	}

	var syntaxErr *json.SyntaxError
	if _, ok, err := c.Get(ctx, "k"); ok || !errors.As(err, &syntaxErr) {
		t.Fatalf("Get = (ok %v, err %v), want (false, *json.SyntaxError)", ok, err)
	}
	factory, calls := factoryCounter("v")
	if _, err := c.GetOrSet(ctx, "k", factory); !errors.As(err, &syntaxErr) {
		t.Fatalf("GetOrSet error = %v, want *json.SyntaxError", err)
	}
	if n := calls.Load(); n != 0 {
		t.Fatalf("factory called %d times after a rethrown deserialization error, want 0", n)
	}
}

func TestReads_TreatDeserializationFailureAsMissWithoutRethrow(t *testing.T) {
	var logs syncBuffer
	c, l2 := newSerializationCache(t, &logs)
	ctx := context.Background()
	if err := l2.Set(ctx, "k", []byte("not an envelope"), 0, nil); err != nil {
		t.Fatal(err)
	}

	if v, ok, err := c.Get(ctx, "k", noSerializationRethrow); v != nil || ok || err != nil {
		t.Fatalf("Get = (%v, %v, %v), want a clean miss", v, ok, err)
	}
	factory, calls := factoryCounter("v")
	if v, err := c.GetOrSet(ctx, "k", factory, noSerializationRethrow); err != nil || v != "v" {
		t.Fatalf("GetOrSet = (%v, %v), want (v, nil)", v, err)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("factory called %d times, want 1", n)
	}
}
