package cache

import "fmt"

// ErrFactoryHardTimeout is returned by [Cache.GetOrSet] when the factory
// exceeds [EntryOptions.FactoryHardTimeout] and no stale fail-safe value
// is available.
//
// Use [errors.Is] to check:
//
//	if errors.Is(err, cache.ErrFactoryHardTimeout) {
//	    // handle timeout
//	}
var ErrFactoryHardTimeout = fmt.Errorf("cache: factory hard timeout")

// ErrLockTimeout is returned when [EntryOptions.LockTimeout] elapses before
// the stampede protection lock is acquired. The caller may proceed without
// the lock (best-effort).
//
// Use [errors.Is] to check:
//
//	if errors.Is(err, cache.ErrLockTimeout) {
//	    // handle lock timeout
//	}
var ErrLockTimeout = fmt.Errorf("cache: stampede lock timeout")
