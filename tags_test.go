package cache

import (
	"context"
	"testing"
)

func TestTags_DeleteByTag(t *testing.T) {
	c, _ := newTestCache(t)
	ctx := context.Background()

	c.Set(ctx, "p1", "product1", WithTags("featured", "electronics"))
	c.Set(ctx, "p2", "product2", WithTags("featured", "clothing"))
	c.Set(ctx, "p3", "product3", WithTags("electronics"))

	// Delete all "featured" entries
	c.DeleteByTag(ctx, "featured")

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
