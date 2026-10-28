package cache

import (
	"context"
	"log/slog"
	"time"
)

// defaultEagerRefreshTimeout bounds a background eager refresh when no
// [EntryOptions.FactoryHardTimeout] is configured.
const defaultEagerRefreshTimeout = 30 * time.Second

// maybeStartEagerRefresh starts a background factory call for entry once
// [EntryOptions.EagerRefreshThreshold] of its duration has elapsed. At most
// one refresh runs per entry. The refresh keeps ctx's values but not its
// cancellation.
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
			timeout = defaultEagerRefreshTimeout
		}
		refreshCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
		defer cancel()
		refreshOpts := opts
		refreshFctx := &FactoryExecutionContext{
			Options:       &refreshOpts,
			StaleValue:    entry.value,
			HasStaleValue: true,
		}
		newValue, err := factory(refreshCtx, refreshFctx)
		if err != nil {
			c.logger.Warn("cache: eager refresh failed", slog.String("key", key), slog.String("error", err.Error()))
			return
		}
		c.events.emit(EventEagerRefreshComplete{Key: key})
		c.storeSafely(refreshCtx, key, newValue, refreshOpts)
	}()
}
