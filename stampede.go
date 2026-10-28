package cache

import (
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"
)

type stampedeGroup struct {
	sf singleflight.Group
}

// Caller states for DoWithTimeout: whether this caller's own singleflight
// call ran fn, or the caller gave up on the lock first.
const (
	callPending int32 = iota
	callExecuting
	callAbandoned
)

// Do executes fn exactly once per key across concurrent callers.
// All callers for the same key block until the single execution completes,
// then receive the same result. executed reports whether this caller ran
// fn itself rather than receiving another caller's result.
func (g *stampedeGroup) Do(key string, fn func() (any, error)) (v any, executed bool, err error) {
	v, err, _ = g.sf.Do(key, func() (any, error) {
		executed = true // runs on this goroutine, only when this caller leads
		return fn()
	})
	return v, executed, err
}

// DoWithTimeout is like Do, but if another caller holds the lock for key
// longer than timeout, this caller stops waiting and runs fn itself,
// without the lock; the in-flight call continues for its own waiters. A
// caller whose own call is running fn (no contention, just a slow fn)
// keeps waiting for it, so fn never runs twice for one caller. executed
// reports whether this caller ran fn (inside its own call or bypassing).
func (g *stampedeGroup) DoWithTimeout(
	key string,
	timeout time.Duration,
	fn func() (any, error),
) (v any, executed bool, err error) {
	if timeout == 0 {
		return g.Do(key, fn)
	}

	type result struct {
		v   any
		err error
	}

	var state atomic.Int32
	ch := make(chan result, 1) // buffered: the goroutine never blocks

	go func() {
		v, err, _ := g.sf.Do(key, func() (any, error) {
			// If the caller already gave up, this call still leads the key
			// and must produce a result for any waiters that joined it.
			state.CompareAndSwap(callPending, callExecuting)
			return fn()
		})
		ch <- result{v, err}
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case r := <-ch:
		return r.v, state.Load() == callExecuting, r.err
	case <-timer.C:
		if !state.CompareAndSwap(callPending, callAbandoned) {
			// This caller's own call is running fn: wait for it.
			r := <-ch
			return r.v, true, r.err
		}
		v, err := fn()
		return v, true, err
	}
}
