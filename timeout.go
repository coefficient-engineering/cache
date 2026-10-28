package cache

import (
	"context"
	"fmt"
	"time"
)

// defaultBackgroundFactoryTimeout bounds how long a factory that outlived
// its soft timeout may keep running in the background when no
// [EntryOptions.FactoryHardTimeout] is configured.
const defaultBackgroundFactoryTimeout = 60 * time.Second

// factoryCall groups the inputs of a single factory execution.
type factoryCall struct {
	// key is the unprefixed cache key being produced.
	key string
	// staleEntry is the fail-safe fallback; nil when none is available.
	staleEntry *cacheEntry
	// opts is a snapshot of the entry options taken before the factory runs.
	opts EntryOptions
	// factory produces the fresh value.
	factory FactoryFunc
	// fctx is passed to factory; fctx.Options may be mutated by it.
	fctx *FactoryExecutionContext
}

// hasStaleFallback reports whether a stale value may be served on failure.
func (fc *factoryCall) hasStaleFallback() bool {
	return fc.opts.IsFailSafeEnabled && fc.staleEntry != nil
}

// factoryResult is the outcome of a factory invocation.
type factoryResult struct {
	value any
	err   error
}

// runFactoryWithTimeouts calls the factory, applying the soft and hard
// factory timeouts from call.opts.
func (c *cache) runFactoryWithTimeouts(ctx context.Context, call *factoryCall) (any, error) {
	opts := &call.opts
	// Fast path: no timeouts configured.
	if opts.FactorySoftTimeout == 0 && opts.FactoryHardTimeout == 0 {
		return call.factory(ctx, call.fctx)
	}

	resultCh := make(chan factoryResult, 1)
	factoryCtx, factoryCancel := context.WithCancel(ctx)

	go func() {
		v, err := call.factory(factoryCtx, call.fctx)
		resultCh <- factoryResult{v, err}
	}()
	return c.awaitFactory(ctx, call, resultCh, factoryCancel)
}

// factoryTimers returns the soft and hard timeout channels for opts. A nil
// channel (never fires) is returned for a timeout that is not configured.
func factoryTimers(opts *EntryOptions) (softTimer, hardTimer <-chan time.Time) {
	if opts.FactorySoftTimeout > 0 {
		softTimer = time.After(opts.FactorySoftTimeout)
	}
	if opts.FactoryHardTimeout > 0 {
		hardTimer = time.After(opts.FactoryHardTimeout)
	}
	return softTimer, hardTimer
}

// awaitFactory waits for the factory started by runFactoryWithTimeouts,
// serving the stale value or failing when the soft or hard timeout fires.
func (c *cache) awaitFactory(
	ctx context.Context,
	call *factoryCall,
	resultCh <-chan factoryResult,
	factoryCancel context.CancelFunc,
) (any, error) {
	opts := &call.opts
	softTimer, hardTimer := factoryTimers(opts)
	softFired := false

	for {
		select {
		case r := <-resultCh:
			// Factory completed.
			if r.err == nil && softFired && opts.AllowTimedOutFactoryBackgroundCompletion {
				c.storeSafely(context.WithoutCancel(ctx), call.key, r.value, *call.fctx.Options)
			}
			factoryCancel()
			return r.value, r.err

		case <-softTimer:
			softTimer = nil
			softFired = true
			if call.hasStaleFallback() {
				c.handleSoftTimeout(ctx, call, resultCh, factoryCancel)
				return call.staleEntry.value, nil
			}
			// No stale value — keep waiting for factory or hard timeout.

		case <-hardTimer:
			c.events.emit(EventHardTimeoutActivated{Key: call.key})
			factoryCancel()
			if call.hasStaleFallback() {
				return call.staleEntry.value, nil
			}
			return nil, fmt.Errorf("%w: key=%s", ErrFactoryHardTimeout, call.key)

		case <-ctx.Done():
			factoryCancel()
			return nil, ctx.Err()
		}
	}
}

// handleSoftTimeout runs when the soft timeout fires and a stale value will
// be served. If background completion is allowed, the still-running factory
// result is stored when it arrives; otherwise the factory is cancelled.
func (c *cache) handleSoftTimeout(
	ctx context.Context,
	call *factoryCall,
	resultCh <-chan factoryResult,
	factoryCancel context.CancelFunc,
) {
	opts := &call.opts
	c.events.emit(EventSoftTimeoutActivated{Key: call.key})
	if !opts.AllowTimedOutFactoryBackgroundCompletion {
		factoryCancel()
		return
	}

	// Keep goroutine alive; it will call storeSafely on completion.
	safetyTimeout := defaultBackgroundFactoryTimeout
	if opts.FactoryHardTimeout > 0 {
		safetyTimeout = opts.FactoryHardTimeout
	}
	storeCtx := context.WithoutCancel(ctx)
	go func() {
		select {
		case r := <-resultCh:
			if r.err == nil {
				c.storeSafely(storeCtx, call.key, r.value, *call.fctx.Options)
			}
			factoryCancel()
		case <-time.After(safetyTimeout):
			factoryCancel()
		}
	}()
}
