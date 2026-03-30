# Using Auto-Clone

Auto-clone prevents callers from mutating values stored in the L1 cache. When enabled, values returned from L1 are deep-cloned via serialize then deserialize before being handed to the caller.

## Prerequisites

Auto-clone requires a `Serializer` to be configured on the cache. The serializer is used to perform the deep copy (marshal the value to bytes, then unmarshal into a new allocation).

## Enable auto-clone

### Per-call

```go
value, err := cache.GetOrSet(ctx, c, "key", factory,
    cache.WithAutoClone(),
)
```

### As a default

```go
c, err := cache.New(
    cache.WithSerializer(&jsonserializer.Serializer{}),
    cache.WithDefaultEntryOptions(cache.EntryOptions{
        Duration:        5 * time.Minute,
        EnableAutoClone: true,
    }),
)
```

## When to use it

Auto-clone is useful when:

- Cached values are pointers or contain pointers (structs, slices, maps).
- Multiple goroutines call `GetOrSet` or `Get` and may modify the returned value.
- You want to prevent one caller's mutations from affecting the value seen by other callers or the cache itself.

Without auto-clone, all callers sharing a cached pointer value receive the same pointer. A mutation by one caller is visible to all others and corrupts the cached data.

## How it works

1. The value is retrieved from L1 (a `*cacheEntry` containing the original value).
2. The serializer's `Marshal` method encodes the value to `[]byte`.
3. A new value of the same concrete type is allocated via `reflect.New`.
4. The serializer's `Unmarshal` method decodes the bytes into the new allocation.
5. The new allocation is returned to the caller.

This process works for both pointer and non-pointer types. For pointer types (`*T`), `reflect.New` allocates a new `T` and returns a `*T`. For value types, it allocates via `reflect.New(T)` and returns the underlying value.

## Performance cost

Each auto-cloned read involves one marshal and one unmarshal operation. For large values or high-throughput hot paths, this may add measurable overhead. Consider whether the cost is justified for your workload:

- If the value is small and callers never modify it, auto-clone is unnecessary.
- If the value is large and callers may modify it, auto-clone is the correct choice.

## Without a serializer

If `EnableAutoClone` is `true` but no serializer is configured, the value is returned as-is (no clone is performed). No error is returned.
