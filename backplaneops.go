package cache

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/coefficient-engineering/cache/backplane"
)

// publishBackplane publishes a message to the backplane.
func (c *cache) publishBackplane(ctx context.Context, msgType backplane.MessageType, key string, opts EntryOptions) {
	if c.bp == nil || opts.SkipBackplaneNotifications {
		return
	}
	if c.bpCB.IsOpen() {
		return // circuit breaker open
	}
	msg := backplane.Message{
		Type:      msgType,
		CacheName: c.opts.CacheName,
		Key:       key,
		SourceID:  c.opts.NodeID,
	}

	doPublish := func() {
		err := c.bp.Publish(ctx, msg)
		c.bpCB.Record(err)
		if err != nil {
			c.logger.Warn("cache: backplane publish failed",
				slog.String("key", key),
				slog.String("type", fmt.Sprintf("%d", msgType)),
				slog.String("error", err.Error()),
			)
		} else {
			c.events.emit(EventBackplaneSent{Key: key, Type: msgType})
		}
	}
	if opts.AllowBackgroundBackplaneOperations {
		go doPublish()
	} else {
		doPublish()
	}
}

// handleBackplaneMessage processes an incoming backplane message.
func (c *cache) handleBackplaneMessage(msg backplane.Message) {
	// only process messages for our cache name
	if msg.CacheName != c.opts.CacheName {
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
	c.events.emit(EventBackplaneReceived{Key: msg.Key, Type: msg.Type})

	switch msg.Type {
	case backplane.MessageTypeSet, backplane.MessageTypeDelete:
		// another node wrote or deleted this key, evict from l1 so the next
		// read gets fresh data from l2
		c.evictL1(c.prefixedKey(msg.Key))

	case backplane.MessageTypeExpire:
		c.expireL1(c.prefixedKey(msg.Key))

	case backplane.MessageTypeClear:
		c.l1.Clear()
		c.tagIdx.clear()
	}
}

func (c *cache) attemptAutoRecovery() {
	if !c.opts.BackplaneAutoRecovery {
		return
	}
	c.logger.Info("cache: backplane auto-recovery, clearing L1...")
	c.l1.Clear()
	c.tagIdx.clear()
}
