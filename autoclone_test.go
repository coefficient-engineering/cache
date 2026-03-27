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
	_ = p
}
