package cache

import "fmt"

// ErrFactoryHardTimeout is returned when the factory exceeds the hard timeout
// and no stale fail safe value is available.
var ErrFactoryHardTimeout = fmt.Errorf("cache: factory hard timeout")

// ErrLockTimeout is returned when LockTImeout elapses before the stampede
// lock is acquried. The caller may proceed without the lokc (best-effort)
var ErrLockTimeout = fmt.Errorf("cache: stampede lock timeout")
