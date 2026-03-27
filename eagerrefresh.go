package cache

import (
	"context"
	"log/slog"
	"time"
)

func (c *cache) maybeStartEagerRefresh(
	ctx context.Context,
	key string,
	entry *cacheEntry,
	opts EntryOptions,
	factory FactoryFunc,
) {
	t := opts.EagerRefreshThreshold
	if t <= 0 || t >= 1.0 {
		return // disabled
	}
	total := entry.logicalExpiry.Sub(entry.createdAt)
	if total <= 0 {
		return
	}
	elapsed := c.clock.Now().Sub(entry.createdAt)
	if float32(elapsed)/float32(total) < t {
		return // not yet past threshold
	}

	// atomic cas, only one goroutine starts a refresh per entry
	if !entry.eagerRefreshRunning.CompareAndSwap(0, 1) {
		return
	}

	c.logger.Debug("cache: eager refresh starting", slog.String("key", key))
	c.events.emit(EventEagerRefreshStarted{Key: key})

	go func() {
		defer entry.eagerRefreshRunning.Store(0)
		timeout := opts.FactoryHardTimeout
		if timeout == 0 {
			timeout = 30 * time.Second
		}
		refreshCtx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		newValue, err := factory(refreshCtx)
		if err != nil {
			c.logger.Warn("cache: eager refresh failed", slog.String("key", key), slog.String("error", err.Error()))
			return
		}
		c.events.emit(EventEagerRefreshComplete{Key: key})
		c.storeSafely(refreshCtx, key, newValue, opts)
	}()
}
