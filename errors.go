package cache

import "errors"

// ErrFactoryHardTimeout is returned by [Cache.GetOrSet] when the factory
// exceeds [EntryOptions.FactoryHardTimeout] and no stale fail-safe value
// is available.
//
// Use [errors.Is] to check:
//
//	if errors.Is(err, cache.ErrFactoryHardTimeout) {
//	    // handle timeout
//	}
var ErrFactoryHardTimeout = errors.New("cache: factory hard timeout")

// ErrLockTimeout is returned when [EntryOptions.LockTimeout] elapses before
// the stampede protection lock is acquired. The caller may proceed without
// the lock (best-effort).
//
// Use [errors.Is] to check:
//
//	if errors.Is(err, cache.ErrLockTimeout) {
//	    // handle lock timeout
//	}
var ErrLockTimeout = errors.New("cache: stampede lock timeout")
