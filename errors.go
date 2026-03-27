package cache

import "fmt"

// ErrFactoryHardTimeout is returned when the factory exceeds the hard timeout
// and no stale fail safe value is available.
var ErrFactoryHardTimeout = fmt.Errorf("cache: factory hard timeout")

// ErrLockTimeout is returned when LockTimeout elapses before the stampede
// lock is acquired. The caller may proceed without the lock (best-effort).
var ErrLockTimeout = fmt.Errorf("cache: stampede lock timeout")
