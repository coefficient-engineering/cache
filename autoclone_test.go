package cache

import (
	"context"
	"testing"
	"time"

	jsonserializer "github.com/coefficient-engineering/cache/adapters/serializer/json"
)

func TestAutoClone_PreventsMutation(t *testing.T) {
	c, _ := newTestCache(t,
		WithSerializer(&jsonserializer.Serializer{}),
		WithDefaultEntryOptions(EntryOptions{
			Duration:        5 * time.Minute,
			EnableAutoClone: true,
		}),
	)
	ctx := context.Background()

	type Product struct {
		Name  string
		Price int
	}

	c.Set(ctx, "p1", &Product{Name: "Widget", Price: 100})

	// Get a clone
	val, ok, _ := c.Get(ctx, "p1")
	if !ok {
		t.Fatal("expected hit")
	}

	// Mutate the returned value
	p := val.(*Product)
	p.Price = 999

	// Re-fetch and verify original is unchanged
	val2, ok2, err := c.Get(ctx, "p1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok2 {
		t.Fatal("expected hit on second get")
	}
	p2 := val2.(*Product)
	if p2.Price != 100 {
		t.Errorf("expected original price 100, got %d (mutation was not prevented)", p2.Price)
	}
}
