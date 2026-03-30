# Using Fail-Safe

Fail-safe returns a stale cached value when the factory function fails, instead of propagating the error to the caller. This keeps your application serving data during transient backend outages.

## Enable fail-safe

### As a cache-wide default

```go
c, err := cache.New(
    cache.WithDefaultEntryOptions(cache.EntryOptions{
        Duration:                 5 * time.Minute,
        IsFailSafeEnabled:       true,
        FailSafeMaxDuration:     2 * time.Hour,
        FailSafeThrottleDuration: 30 * time.Second,
    }),
)
```

### Per-call override

```go
value, err := cache.GetOrSet(ctx, c, "key", factory,
    cache.WithFailSafe(2*time.Hour, 30*time.Second),
)
```

`WithFailSafe` sets `IsFailSafeEnabled` to `true`, `FailSafeMaxDuration` to the first argument, and `FailSafeThrottleDuration` to the second.

## Configuration fields

**`IsFailSafeEnabled`** (bool): Activates fail-safe for this entry. When `false`, factory errors are always returned to the caller.

**`FailSafeMaxDuration`** (time.Duration): The total physical lifetime of the entry in the cache. The entry is logically stale after `Duration` but physically present (and available as a fallback) for `FailSafeMaxDuration`. Must be greater than or equal to `Duration`.

**`FailSafeThrottleDuration`** (time.Duration): After a factory failure, the stale value is temporarily promoted back to "fresh" in L1 for this duration. This prevents the factory from being called on every request during an outage.

**`AllowStaleOnReadOnly`** (bool): When `true`, the read-only `Get` method returns stale entries instead of `(nil, false, nil)`. Use `WithAllowStaleOnReadOnly()` to set this.

## How it works

1. A `GetOrSet` call finds a stale entry in L1 or L2 (past `Duration` but within `FailSafeMaxDuration`).
2. The factory is called to get a fresh value.
3. If the factory fails and fail-safe is enabled, the stale value is returned to the caller.
4. The stale entry is re-inserted into L1 with a short TTL equal to `FailSafeThrottleDuration`, suppressing further factory calls for that window.

## Example: surviving a database outage

```go
c, err := cache.New(
    cache.WithDefaultEntryOptions(cache.EntryOptions{
        Duration:                 1 * time.Minute,
        IsFailSafeEnabled:       true,
        FailSafeMaxDuration:     1 * time.Hour,
        FailSafeThrottleDuration: 30 * time.Second,
    }),
)

value, err := cache.GetOrSet(ctx, c, "user:42", func(ctx context.Context) (*User, error) {
    return db.GetUser(ctx, 42) // fails during outage
})
// If a stale value exists, err is nil and value contains the stale user.
// The factory will not be retried for 30 seconds.
```

## Fail-safe with timeouts

Fail-safe interacts with soft and hard timeouts. If a soft timeout fires and a stale value is available, the stale value is returned immediately while the factory continues in the background. See [Using Timeouts](UsingTimeouts.md).

## Fail-safe on read-only Get

By default, `Get` does not return stale entries. To allow it:

```go
value, found, err := cache.Get[string](ctx, c, "key",
    cache.WithAllowStaleOnReadOnly(),
)
```

## See also

- [Fail-Safe Mechanism](../explanation/FailSafeMechanism.md) for how logical and physical expiry work
- [Using Timeouts](UsingTimeouts.md) for soft/hard timeout interaction
