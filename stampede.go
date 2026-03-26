package cache

import (
	"fmt"
	"time"

	"golang.org/x/sync/singleflight"
)

// ErrLockTimeout is returned when LockTImeout elapses before the stampede
// lock is acquried. The caller may proceed without the lokc (best-effort)
var ErrLockTimeout = fmt.Errorf("cache: stampede lock timeout")

type stampedeGroup struct {
	sf singleflight.Group
}

// Do executes fn exactly once per key across concurrent callers.
// All callers for the same key blocks until the single execution completes,
// then receive the same result.
func (g *stampedeGroup) Do(key string, fn func() (any, error)) (any, error, bool) {
	return g.sf.Do(key, fn)
}

// DoWithTimeout is like Do, but the caller gives up waiting after timeout and
// receives ErrLockTimeout. The in-flight call continues; its result is stored
// in the cache when it completes (provided AllowTimeOutFactoryBackgroundCompletion
// is true), benefiting future callers.
func (g *stampedeGroup) DoWithTimeout(
	key string,
	timeout time.Duration,
	fn func() (any, error),
) (any, error, bool) {
	if timeout == 0 {
		return g.sf.Do(key, fn)
	}

	type result struct {
		v   any
		err error
	}

	ch := make(chan result, 1)

	go func() {
		v, err, _ := g.sf.Do(key, fn)
		ch <- result{v, err}
	}()

	select {
	case r := <-ch:
		return r.v, r.err, false
	case <-time.After(timeout):
		return nil, ErrLockTimeout, false
	}
}
