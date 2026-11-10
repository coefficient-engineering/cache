package cache

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	memoryl2 "github.com/coefficient-engineering/cache/adapters/l2/memory"
	jsonserializer "github.com/coefficient-engineering/cache/adapters/serializer/json"
)

func TestTags_DeleteByTag(t *testing.T) {
	c, _ := newTestCache(t)
	ctx := context.Background()

	if err := c.Set(ctx, "p1", "product1", WithTags("featured", "electronics")); err != nil {
		t.Fatal(err)
	}
	if err := c.Set(ctx, "p2", "product2", WithTags("featured", "clothing")); err != nil {
		t.Fatal(err)
	}
	if err := c.Set(ctx, "p3", "product3", WithTags("electronics")); err != nil {
		t.Fatal(err)
	}

	// Delete all "featured" entries
	if err := c.DeleteByTag(ctx, "featured"); err != nil {
		t.Fatal(err)
	}

	_, ok1, _ := c.Get(ctx, "p1")
	_, ok2, _ := c.Get(ctx, "p2")
	_, ok3, _ := c.Get(ctx, "p3")

	if ok1 {
		t.Error("p1 should be deleted (tagged featured)")
	}
	if ok2 {
		t.Error("p2 should be deleted (tagged featured)")
	}
	if !ok3 {
		t.Error("p3 should NOT be deleted (not tagged featured)")
	}
}

// tagsDeleterL2 is an in-memory L2 that implements l2.TagsDeleter and
// records how its tags are deleted.
type tagsDeleterL2 struct {
	*memoryl2.Adapter

	mu        sync.Mutex
	tagCalls  int
	tagsCalls [][]string
}

func (a *tagsDeleterL2) DeleteByTag(ctx context.Context, tag string) error {
	a.mu.Lock()
	a.tagCalls++
	a.mu.Unlock()
	return a.Adapter.DeleteByTag(ctx, tag)
}

func (a *tagsDeleterL2) DeleteByTags(ctx context.Context, tags []string) error {
	a.mu.Lock()
	a.tagsCalls = append(a.tagsCalls, slices.Clone(tags))
	a.mu.Unlock()
	for _, tag := range tags {
		if err := a.Adapter.DeleteByTag(ctx, tag); err != nil {
			return err
		}
	}
	return nil
}

func TestTags_DeleteByTags(t *testing.T) {
	// The memory L2 does not implement l2.TagsDeleter, so each tag is
	// deleted with DeleteByTag.
	opts := []Option{WithL2(memoryl2.New()), WithSerializer(&jsonserializer.Serializer{})}
	c, _ := newTestCache(t, opts...)
	// other shares the L2 but not the L1, so it only sees L2 entries.
	other, _ := newTestCache(t, opts...)
	ctx := context.Background()

	if err := c.Set(ctx, "p1", "product1", WithTags("featured")); err != nil {
		t.Fatal(err)
	}
	if err := c.Set(ctx, "p2", "product2", WithTags("sale")); err != nil {
		t.Fatal(err)
	}
	if err := c.Set(ctx, "p3", "product3", WithTags("featured", "sale")); err != nil {
		t.Fatal(err)
	}
	if err := c.Set(ctx, "p4", "product4", WithTags("clothing")); err != nil {
		t.Fatal(err)
	}

	if err := c.DeleteByTags(ctx, []string{"sale", "featured", "sale"}); err != nil {
		t.Fatal(err)
	}

	for _, cache := range []Cache{c, other} {
		for _, key := range []string{"p1", "p2", "p3"} {
			if _, ok, _ := cache.Get(ctx, key); ok {
				t.Errorf("%s should be deleted (tagged featured or sale)", key)
			}
		}
		if _, ok, _ := cache.Get(ctx, "p4"); !ok {
			t.Error("p4 should NOT be deleted (tagged clothing)")
		}
	}
}

func TestTags_DeleteByTagsUsesTagsDeleter(t *testing.T) {
	l2 := &tagsDeleterL2{Adapter: memoryl2.New()}
	c, _ := newTestCache(t, WithL2(l2), WithSerializer(&jsonserializer.Serializer{}))
	ctx := context.Background()

	if err := c.Set(ctx, "p1", "product1", WithTags("featured")); err != nil {
		t.Fatal(err)
	}
	if err := c.Set(ctx, "p2", "product2", WithTags("sale")); err != nil {
		t.Fatal(err)
	}

	if err := c.DeleteByTags(ctx, []string{"sale", "featured", "sale"}); err != nil {
		t.Fatal(err)
	}
	// No tags is a no-op.
	if err := c.DeleteByTags(ctx, nil); err != nil {
		t.Fatal(err)
	}

	for _, key := range []string{"p1", "p2"} {
		if _, ok, _ := c.Get(ctx, key); ok {
			t.Errorf("%s should be deleted", key)
		}
	}
	l2.mu.Lock()
	defer l2.mu.Unlock()
	want := [][]string{{"featured", "sale"}}
	if !slices.EqualFunc(l2.tagsCalls, want, slices.Equal) {
		t.Fatalf("DeleteByTags calls = %v, want %v", l2.tagsCalls, want)
	}
	if l2.tagCalls != 0 {
		t.Fatalf("DeleteByTag calls = %d, want 0", l2.tagCalls)
	}
}

// fallbackL2 is an in-memory L2 without l2.TagsDeleter. It records the
// tags passed to DeleteByTag and runs onTag, when set, instead of deleting.
type fallbackL2 struct {
	*memoryl2.Adapter

	onTag func(ctx context.Context, tag string) error

	mu   sync.Mutex
	tags []string
}

func (a *fallbackL2) DeleteByTag(ctx context.Context, tag string) error {
	a.mu.Lock()
	a.tags = append(a.tags, tag)
	a.mu.Unlock()
	if a.onTag != nil {
		return a.onTag(ctx, tag)
	}
	return a.Adapter.DeleteByTag(ctx, tag)
}

func (a *fallbackL2) calledTags() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return slices.Clone(a.tags)
}

func TestTags_DeleteByTagsFallbackNamesFailedTag(t *testing.T) {
	errTag := errors.New("tag failed")
	l2 := &fallbackL2{Adapter: memoryl2.New()}
	l2.onTag = func(ctx context.Context, tag string) error {
		if tag == "u" {
			return errTag
		}
		return l2.Adapter.DeleteByTag(ctx, tag)
	}
	c, _ := newTestCache(t, WithL2(l2), WithSerializer(&jsonserializer.Serializer{}))
	ctx := context.Background()

	err := c.DeleteByTags(ctx, []string{"v", "t", "u"}, rethrowL2)
	if !errors.Is(err, errTag) {
		t.Fatalf("DeleteByTags error = %v, want %v", err, errTag)
	}
	if !strings.Contains(err.Error(), `tag "u": tag failed`) {
		t.Fatalf("DeleteByTags error = %q, want it to name tag u", err)
	}
	// A failed tag does not stop the others.
	if got, want := l2.calledTags(), []string{"t", "u", "v"}; !slices.Equal(got, want) {
		t.Fatalf("DeleteByTag tags = %v, want %v", got, want)
	}
}

func TestTags_DeleteByTagsFallbackStopsWhenContextDone(t *testing.T) {
	tests := []struct {
		name    string
		opts    []EntryOption
		onTag   func(ctx context.Context, tag string, cancel context.CancelFunc) error
		wantErr error
		want    []string
	}{
		{
			name: "canceled",
			opts: []EntryOption{rethrowL2},
			onTag: func(_ context.Context, tag string, cancel context.CancelFunc) error {
				if tag == "b" {
					cancel()
				}
				return nil
			},
			wantErr: context.Canceled,
			want:    []string{"a", "b"},
		},
		{
			name: "hard timeout",
			opts: []EntryOption{rethrowL2, WithDistributedCacheTimeouts(0, 10*time.Millisecond)},
			onTag: func(ctx context.Context, _ string, _ context.CancelFunc) error {
				<-ctx.Done()
				return ctx.Err()
			},
			wantErr: context.DeadlineExceeded,
			want:    []string{"a"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			l2 := &fallbackL2{Adapter: memoryl2.New()}
			l2.onTag = func(ctx context.Context, tag string) error { return tt.onTag(ctx, tag, cancel) }
			c, _ := newTestCache(t, WithL2(l2), WithSerializer(&jsonserializer.Serializer{}))

			err := c.DeleteByTags(ctx, []string{"c", "b", "a"}, tt.opts...)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("DeleteByTags error = %v, want %v", err, tt.wantErr)
			}
			if n := strings.Count(err.Error(), tt.wantErr.Error()); n != 1 {
				t.Fatalf("DeleteByTags error = %q, want %q once, got %d", err, tt.wantErr, n)
			}
			if got := l2.calledTags(); !slices.Equal(got, tt.want) {
				t.Fatalf("DeleteByTag tags = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestTagsSummary(t *testing.T) {
	tests := []struct {
		tags []string
		want string
	}{
		{tags: []string{"a"}, want: "a"},
		{tags: []string{"a", "b", "c"}, want: "a,b,c"},
		{tags: []string{"a", "b", "c", "d", "e"}, want: "a,b,c (+2 more)"},
	}
	for _, tt := range tests {
		if got := tagsSummary(tt.tags); got != tt.want {
			t.Errorf("tagsSummary(%q) = %q, want %q", tt.tags, got, tt.want)
		}
	}
}

func TestTagIndex_AddRemove(t *testing.T) {
	var ti tagIndex

	ti.add("key1", []string{"a", "b"})
	ti.add("key2", []string{"b", "c"})

	keys := ti.keysForTag("b")
	if len(keys) != 2 {
		t.Fatalf("expected 2 keys for tag b, got %d", len(keys))
	}

	ti.remove("key1", []string{"a", "b"})
	keys = ti.keysForTag("b")
	if len(keys) != 1 {
		t.Fatalf("expected 1 key for tag b after remove, got %d", len(keys))
	}
	keys = ti.keysForTag("a")
	if len(keys) != 0 {
		t.Fatalf("expected 0 keys for tag a after remove, got %d", len(keys))
	}
}
