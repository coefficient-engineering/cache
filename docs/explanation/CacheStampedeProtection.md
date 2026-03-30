# Cache Stampede Protection

## The problem

A cache stampede (also called a thundering herd) occurs when a popular cache entry expires and many concurrent requests simultaneously discover the miss. Without protection, all of them call the factory function at the same time, potentially overwhelming the upstream data source with redundant work.

For example: 100 goroutines request the same expired key within the same millisecond. Without stampede protection, all 100 call the database. With stampede protection, one calls the database and the other 99 wait for that single result.

## How the cache solves it

The cache uses `golang.org/x/sync/singleflight` to deduplicate concurrent factory calls for the same key. When multiple goroutines call `GetOrSet` with the same key simultaneously:

1. The first goroutine acquires the singleflight lock for that key.
2. All subsequent goroutines for the same key block, waiting.
3. The first goroutine executes the factory.
4. When the factory returns, all waiting goroutines receive the same result.

Only one factory call is made per key at any given time.

## The singleflight wrapper

The `stampedeGroup` type in `stampede.go` wraps `singleflight.Group` with two methods:

- `Do(key, fn)`: Standard deduplication. Blocks until the in-flight call completes.
- `DoWithTimeout(key, timeout, fn)`: Like `Do`, but the caller gives up waiting after `timeout` and receives `ErrLockTimeout`. The in-flight factory continues running. When it eventually completes, its result is stored in the cache, benefiting future callers.

## Lock timeout

The `LockTimeout` entry option controls how long a goroutine waits for the singleflight lock. If the timeout elapses, the goroutine receives `ErrLockTimeout` instead of waiting indefinitely. This prevents starvation when a factory call is unusually slow.

Setting `LockTimeout` trades a small risk of duplicate factory calls against guaranteed bounded latency. In most cases, leaving it at the default (0, meaning wait indefinitely) is correct.

## Re-check after acquiring the lock

Inside the singleflight function, the cache re-checks L1 before calling the factory. Another goroutine may have populated the cache while the current one was waiting for the lock. This avoids unnecessary factory calls when singleflight expires between the outer L1 check and entering the critical section.

## Interaction with other features

- **Fail-safe:** If the factory fails inside singleflight, fail-safe returns the stale value to all waiting goroutines.
- **Soft/hard timeouts:** Timeout machinery runs inside the singleflight-protected function. If the soft timeout fires, the stale value is returned to the original goroutine, but the factory continues. The singleflight lock is not released until the factory finishes or the hard timeout fires.
- **Eager refresh:** Eager refresh runs outside singleflight. It starts a background goroutine that calls the factory independently, so it does not block the original caller or other waiters.

## When stampede protection is bypassed

Setting `SkipL1Read: true` bypasses L1 entirely, which also removes the singleflight protection. Use this option with care.

## See also

- [GetOrSet Execution Flow](GetOrSetExecutionFlow.md) for where stampede protection fits in the full path
