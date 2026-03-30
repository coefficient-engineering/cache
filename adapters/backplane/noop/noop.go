// Package noop provides a [backplane.Backplane] that silently discards all
// messages.
//
// Use this for explicit single-node deployments where you want to make it
// clear that no inter-node communication occurs:
//
//	bp := noop.New()
package noop

import (
	"context"

	"github.com/coefficient-engineering/cache/backplane"
)

// Backplane is a no-op backplane that discards all messages.
// All methods are no-ops. [Backplane.Subscribe] returns a no-op cancel function.
type Backplane struct{}

// New returns a new no-op [Backplane].
func New() *Backplane { return &Backplane{} }

func (b *Backplane) Publish(_ context.Context, _ backplane.Message) error {
	return nil
}

func (b *Backplane) Subscribe(_ backplane.Handler) (cancel func(), err error) {
	return func() {}, nil
}

func (b *Backplane) Close() error { return nil }

var _ backplane.Backplane = (*Backplane)(nil)
