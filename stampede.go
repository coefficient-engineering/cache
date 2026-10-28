package cache

import (
	"time"

	"golang.org/x/sync/singleflight"
)

type stampedeGroup struct {
	sf singleflight.Group
}

// Do executes fn exactly once per key across concurrent callers.
// All callers for the same key block until the single execution completes,
// then receive the same result.
func (g *stampedeGroup) Do(key string, fn func() (any, error)) (any, error, bool) {
	return g.sf.Do(key, fn)
}

// DoWithTimeout is like Do, but the caller proceeds without the lock after
// timeout. The in-flight call continues; both calls independently execute fn.
func (g *stampedeGroup) DoWithTimeout(
	key string,
	timeout time.Duration,
	fn func() (any, error),
) (any, error, bool) {
	if timeout == 0 {
		return g.sf.Do(key, fn)
	}

	type result struct {
		v      any
		err    error
		shared bool
	}

	ch := make(chan result, 1)

	go func() {
		v, err, shared := g.sf.Do(key, fn)
		ch <- result{v, err, shared}
	}()

	select {
	case r := <-ch:
		return r.v, r.err, r.shared
	case <-time.After(timeout):
		v, err := fn()
		return v, err, false
	}
}
