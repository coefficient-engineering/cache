package cache

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/coefficient-engineering/cache/backplane"
)

// publishBackplane publishes a message to the backplane. It returns the
// publish error only when the publish ran synchronously and
// ReThrowBackplaneExceptions is set; otherwise failures are logged and nil
// is returned.
func (c *cache) publishBackplane(ctx context.Context, msg backplane.Message, opts EntryOptions) error {
	if c.bp == nil || opts.SkipBackplaneNotifications {
		return nil
	}
	if c.bpCB.IsOpen() {
		return nil // circuit breaker open
	}
	msg.CacheName = c.opts.CacheName
	msg.KeyPrefix = c.opts.KeyPrefix
	msg.SourceID = c.opts.NodeID

	doPublish := func() error {
		err := c.bp.Publish(ctx, msg)
		c.bpCB.Record(err)
		if err != nil {
			c.logger.Warn("cache: backplane publish failed",
				slog.String("key", msg.Key),
				slog.String("type", fmt.Sprintf("%d", msg.Type)),
				slog.String("error", err.Error()),
			)
			return fmt.Errorf("cache: backplane publish (type %d, key %q): %w", msg.Type, msg.Key, err)
		}
		c.events.emit(EventBackplaneSent{Key: msg.Key, Type: msg.Type})
		return nil
	}
	if opts.AllowBackgroundBackplaneOperations {
		go func() { _ = doPublish() }() // background: failure already logged
		return nil
	}
	if err := doPublish(); err != nil && opts.ReThrowBackplaneExceptions {
		return err
	}
	return nil
}

// handleBackplaneMessage processes an incoming backplane message.
func (c *cache) handleBackplaneMessage(msg backplane.Message) {
	// A cache name and key prefix together identify one L1 namespace.
	if msg.CacheName != c.opts.CacheName || msg.KeyPrefix != c.opts.KeyPrefix {
		return
	}
	// ignore msgs from ourselves
	if msg.SourceID == c.opts.NodeID {
		return
	}
	// user opt out
	if c.opts.IgnoreIncomingBackplaneNotifications {
		return
	}

	switch msg.Type {
	case backplane.MessageTypeSet, backplane.MessageTypeDelete:
		// another node wrote or deleted this key, evict from l1 so the next
		// read gets fresh data from l2
		c.evictL1(c.prefixedKey(msg.Key))

	case backplane.MessageTypeExpire:
		c.expireL1(c.prefixedKey(msg.Key))

	case backplane.MessageTypeDeleteByTags:
		c.evictL1Tags(msg.Tags)

	case backplane.MessageTypeClear:
		c.clearL1()
	}
	// Observers can use receipt as a barrier: local invalidation is complete.
	c.events.emit(EventBackplaneReceived{Key: msg.Key, Type: msg.Type})
}

func (c *cache) attemptAutoRecovery() {
	if !c.opts.BackplaneAutoRecovery {
		return
	}
	c.logger.Info("cache: backplane auto-recovery, clearing L1...")
	c.clearL1()
}
