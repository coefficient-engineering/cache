package cache

import (
	"context"
	"log/slog"
	"time"
)

// executeWithFailSafe runs the factory and, on failure, falls back to staleEntry
// if fail-safe is enabled and a stale value is available.
func (c *cache) executeWithFailSafe(
	ctx context.Context,
	key string,
	staleEntry *cacheEntry,
	opts EntryOptions,
	factory FactoryFunc,
	fctx *FactoryExecutionContext,
) (any, error) {
	value, err := c.runFactoryWithTimeouts(ctx, key, staleEntry, opts, factory, fctx)
	if err == nil {
		return value, nil
	}

	if !opts.IsFailSafeEnabled || staleEntry == nil {
		return nil, err // no fallback available
	}
	c.logger.Warn("cache: fail-safe activated, returning stale value",
		slog.String("key", key),
		slog.String("error", err.Error()),
	)

	c.events.emit(EventFailSafeActivated{Key: key, StaleValue: staleEntry.value})

	// re insert stale value under a short ThrottleDuration so we don't
	// call the factory again on every incoming req. during an outage
	throttled := &cacheEntry{
		value:          staleEntry.value,
		logicalExpiry:  time.Now().Add(opts.FailSafeThrottleDuration),
		physicalExpiry: staleEntry.physicalExpiry,
		tags:           staleEntry.tags,
		createdAt:      staleEntry.createdAt,
	}
	c.l1.Set(c.prefixedKey(key), throttled, opts.Size)

	return staleEntry.value, nil
}
