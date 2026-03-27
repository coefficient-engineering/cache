// Package noop provides a backplane that silently discards all messages.
// Use this for explicit single-node deployments where you want to be clear
// that no inter-node communication occurs.
package noop

import (
	"context"

	"github.com/coefficient-engineering/cache/backplane"
)

// Backplane is a no-op backplane that discards all messages.
type Backplane struct{}

func New() *Backplane { return &Backplane{} }

func (b *Backplane) Publish(_ context.Context, _ backplane.Message) error {
	return nil
}

func (b *Backplane) Subscribe(_ backplane.Handler) (cancel func(), err error) {
	return func() {}, nil
}

func (b *Backplane) Close() error { return nil }

var _ backplane.Backplane = (*Backplane)(nil)
