package cache

import (
	"context"
	"fmt"
	"time"
)

func (c *cache) runFactoryWithTimeouts(
	ctx context.Context,
	key string,
	staleEntry *cacheEntry,
	opts EntryOptions,
	factory FactoryFunc,
) (any, error) {
	// Fast path: no timeouts configured.
	if opts.FactorySoftTimeout == 0 && opts.FactoryHardTimeout == 0 {
		return factory(ctx)
	}

	type result struct {
		value any
		err   error
	}

	resultCh := make(chan result, 1)
	factoryCtx, factoryCancel := context.WithCancel(ctx)

	go func() {
		v, err := factory(factoryCtx)
		resultCh <- result{v, err}
	}()

	var softTimer <-chan time.Time
	var hardTimer <-chan time.Time

	if opts.FactorySoftTimeout > 0 {
		softTimer = time.After(opts.FactorySoftTimeout)
	}
	if opts.FactoryHardTimeout > 0 {
		hardTimer = time.After(opts.FactoryHardTimeout)
	}

	softFired := false

	for {
		select {
		case r := <-resultCh:
			// Factory completed.
			if r.err == nil && softFired && opts.AllowTimedOutFactoryBackgroundCompletion {
				c.storeSafely(context.Background(), key, r.value, opts)
			}
			factoryCancel()
			return r.value, r.err

		case <-softTimer:
			softTimer = nil
			softFired = true
			if opts.IsFailSafeEnabled && staleEntry != nil {
				c.events.emit(EventSoftTimeoutActivated{Key: key})
				if opts.AllowTimedOutFactoryBackgroundCompletion {
					// Keep goroutine alive; it will call storeSafely on completion.
					safetyTimeout := 60 * time.Second
					if opts.FactoryHardTimeout > 0 {
						safetyTimeout = opts.FactoryHardTimeout
					}
					go func() {
						select {
						case r := <-resultCh:
							if r.err == nil {
								c.storeSafely(context.Background(), key, r.value, opts)
							}
						case <-time.After(safetyTimeout):
							factoryCancel()
						}
					}()
				} else {
					factoryCancel()
				}
				return staleEntry.value, nil
			}
			// No stale value — keep waiting for factory or hard timeout.

		case <-hardTimer:
			c.events.emit(EventHardTimeoutActivated{Key: key})
			factoryCancel()
			if opts.IsFailSafeEnabled && staleEntry != nil {
				return staleEntry.value, nil
			}
			return nil, fmt.Errorf("%w: key=%s", ErrFactoryHardTimeout, key)

		case <-ctx.Done():
			factoryCancel()
			return nil, ctx.Err()
		}
	}
}
