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

// ErrLockTimeout is retained for source compatibility.
//
// Deprecated: no longer returned. When [EntryOptions.LockTimeout] elapses
// while another caller holds the stampede lock, GetOrSet proceeds without
// the lock instead of failing.
var ErrLockTimeout = errors.New("cache: stampede lock timeout")
