package cache

import (
	"context"
	"log/slog"
	"time"
)

// executeWithFailSafe runs the factory and, on failure, falls back to
// call.staleEntry if fail-safe is enabled and a stale value is available.
func (c *cache) executeWithFailSafe(ctx context.Context, call *factoryCall) (any, error) {
	value, err := c.runFactoryWithTimeouts(ctx, call)
	if err == nil {
		return value, nil
	}

	if !call.hasStaleFallback() {
		return nil, err // no fallback available
	}
	key, staleEntry := call.key, call.staleEntry
	c.logger.Warn("cache: fail-safe activated, returning stale value",
		slog.String("key", key),
		slog.String("error", err.Error()),
	)

	c.events.emit(EventFailSafeActivated{Key: key, StaleValue: staleEntry.value})

	// re insert stale value under a short ThrottleDuration so we don't
	// call the factory again on every incoming req. during an outage
	throttled := &cacheEntry{
		value:          staleEntry.value,
		logicalExpiry:  time.Now().Add(call.opts.FailSafeThrottleDuration),
		physicalExpiry: staleEntry.physicalExpiry,
		tags:           staleEntry.tags,
		createdAt:      staleEntry.createdAt,
	}
	c.l1.Set(c.prefixedKey(key), throttled, call.opts.Size)

	return staleEntry.value, nil
}
