// Package cache provides a hybrid L1+L2 cache for distributed systems,
// inspired by ZiggyCreatures FusionCache.
//
// The core library has zero external dependencies beyond the Go standard
// library and [golang.org/x/sync]. Every pluggable point (the distributed
// cache, the inter-node backplane, and the serializer) is expressed as a
// Go interface. See packages [github.com/coefficient-engineering/cache/l1],
// [github.com/coefficient-engineering/cache/l2],
// [github.com/coefficient-engineering/cache/backplane], and
// [github.com/coefficient-engineering/cache/serializer] for those contracts.
//
// # Quick Start
//
// Create a pure in-process (L1-only) cache with [New] and use the
// type-safe generic helpers [GetOrSet] and [Get]:
//
//	c, err := cache.New(
//	    cache.WithDefaultEntryOptions(cache.EntryOptions{
//	        Duration: 5 * time.Minute,
//	    }),
//	)
//	if err != nil {
//	    log.Fatal(err)
//	}
//	defer c.Close()
//
//	product, err := cache.GetOrSet[*Product](ctx, c, "product:42",
//	    func(ctx context.Context, fctx *cache.FactoryExecutionContext) (*Product, error) {
//	        return db.GetProduct(ctx, 42)
//	    },
//	)
//
// # Generics Strategy
//
// The [Cache] interface stores and returns any. Generic package-level
// functions ([GetOrSet] and [Get]) provide compile-time type safety at
// the call site without complicating the core. Go does not allow generic
// methods on structs, so these helpers are package-level functions.
//
// # Construction
//
// Use [New] to create a [Cache] instance, passing [Option] functions to
// configure it. See [WithL2], [WithSerializer], [WithBackplane], and
// [WithDefaultEntryOptions] for common options.
//
// [New] returns an error if an L2 adapter is configured without a
// serializer.
package cache

import (
	"context"
	"fmt"
	"log/slog"
	"reflect"
	"sync"
	"time"

	"github.com/coefficient-engineering/cache/backplane"
	"github.com/coefficient-engineering/cache/internal/clock"
	"github.com/coefficient-engineering/cache/l1"
	"github.com/coefficient-engineering/cache/l2"
	"github.com/coefficient-engineering/cache/serializer"
)

// Cache is the primary interface for interacting with the cache.
//
// The concrete implementation is created via [New] and is safe
// for concurrent use by multiple goroutines.
type Cache interface { //nolint:interfacebloat // public API mirrors IFusionCache
	// Get returns the cached value for key if present and not logically
	// expired. Returns (nil, false, nil) on a clean cache miss.
	//
	// When [EntryOptions.AllowStaleOnReadOnly] is set, logically expired
	// values are returned with ok == true. The event emitted in that case
	// is [EventCacheHit] with IsStale: true.
	//
	// Get reads L1 first, then L2 if configured and L1 missed.
	// Use the type-safe wrapper [Get] for compile-time type safety.
	Get(ctx context.Context, key string, opts ...EntryOption) (any, bool, error)

	// Set stores value under key in all configured cache layers (L1, L2)
	// and publishes a backplane notification. Skip flags in [EntryOptions]
	// control which layers are written.
	Set(ctx context.Context, key string, value any, opts ...EntryOption) error

	// GetOrSet returns the cached value for key. On a cache miss it calls
	// factory, stores the result, and returns it. This is the primary method.
	//
	// The full execution path:
	//  1. Read L1 (unless [EntryOptions.SkipL1Read])
	//  2. On L1 miss, enter stampede protection (singleflight)
	//  3. Re-check L1 inside the lock
	//  4. Read L2 if configured (unless [EntryOptions.SkipL2Read] or circuit breaker open)
	//  5. Call factory with fail-safe and timeout handling
	//  6. Store result in L1, L2, and notify backplane
	//
	// On a fresh L1 hit with [EntryOptions.EagerRefreshThreshold] configured,
	// a background refresh may be started.
	//
	// Use the type-safe wrapper [GetOrSet] for compile-time type safety.
	GetOrSet(ctx context.Context, key string, factory FactoryFunc, opts ...EntryOption) (any, error)

	// Delete removes the entry from L1 and L2. Publishes a
	// [backplane.MessageTypeDelete] backplane notification.
	// Does not error if the key does not exist.
	Delete(ctx context.Context, key string, opts ...EntryOption) error

	// DeleteByTag removes all entries associated with tag from L1
	// (via the in-memory tag index) and L2 (via [l2.Adapter.DeleteByTag]).
	// Publishes a [backplane.MessageTypeDelete] backplane notification for
	// each removed key.
	DeleteByTag(ctx context.Context, tag string, opts ...EntryOption) error

	// Expire marks the entry as logically expired without removing it from
	// L1. The value remains available as a fail-safe fallback until physical
	// expiry. If fail-safe is not enabled on subsequent reads, the stale
	// value is not returned.
	//
	// Publishes a [backplane.MessageTypeExpire] backplane notification.
	Expire(ctx context.Context, key string, opts ...EntryOption) error

	// Clear removes all entries from L1 and clears the tag index. When
	// clearL2 is true and an L2 adapter is configured, calls
	// [l2.Adapter.Clear]. Publishes a [backplane.MessageTypeClear]
	// backplane notification.
	Clear(ctx context.Context, clearL2 bool) error

	// Name returns the configured cache name. Defaults to "default".
	Name() string

	// DefaultEntryOptions returns a copy of the cache-wide default entry
	// options. Safe to read and modify without affecting the cache.
	DefaultEntryOptions() EntryOptions

	// Events returns the [EventEmitter] for subscribing to cache lifecycle
	// events.
	Events() *EventEmitter

	// Close shuts down background goroutines (backplane subscription) and
	// releases resources. Calls Close on the backplane and L1 adapter.
	// Safe to call multiple times.
	Close() error
}

// FactoryFunc is the function called on a cache miss to produce a fresh value.
// It receives the request context and should respect cancellation.
// Return a non-nil error to signal failure; when fail-safe is enabled and a
// stale value exists, the error is swallowed and the stale value is returned
// instead. See [EntryOptions.IsFailSafeEnabled].
type FactoryFunc func(ctx context.Context, fctx *FactoryExecutionContext) (any, error)

// GetOrSet is the type-safe counterpart to [Cache.GetOrSet].
//
// T can be any type, including pointers and interfaces.
// Returns a type assertion error if the cached value is not of type T.
func GetOrSet[T any](
	ctx context.Context,
	c Cache,
	key string,
	factory func(ctx context.Context, fctx *FactoryExecutionContext) (T, error),
	opts ...EntryOption,
) (T, error) {
	var zero T
	raw, err := c.GetOrSet(ctx, key, func(ctx context.Context, fctx *FactoryExecutionContext) (any, error) {
		return factory(ctx, fctx)
	}, opts...)
	if err != nil {
		return zero, err
	}
	if raw == nil {
		return zero, nil
	}
	v, ok := raw.(T)
	if !ok {
		return zero, fmt.Errorf("cache: type mismatch: want %T, got %T", zero, raw)
	}
	return v, nil
}

// Get is the type-safe counterpart to [Cache.Get].
//
// T can be any type, including pointers and interfaces.
// Returns a type assertion error if the cached value is not of type T.
func Get[T any](ctx context.Context, c Cache, key string, opts ...EntryOption) (T, bool, error) {
	var zero T
	raw, ok, err := c.Get(ctx, key, opts...)
	if err != nil || !ok {
		return zero, ok, err
	}
	v, ok2 := raw.(T)
	if !ok2 {
		return zero, false, fmt.Errorf("cache: type mismatch: want %T, got %T", zero, raw)
	}
	return v, true, nil
}

// cache is the concrete implementation of the [Cache] interface.
// Created via [New]; never instantiated directly. Safe for concurrent use.
type cache struct {
	// config
	opts   Options // full cache-wide config snapshot
	logger *slog.Logger

	l1 l1.Adapter

	l2         l2.Adapter // nil if no L2 configured
	serializer serializer.Serializer

	bp          backplane.Backplane // nil if no backplane configured
	bpCancelSub func()              // cancels the backplane subscription goroutine

	stampede stampedeGroup // per-key dedupe
	l2CB     *circuitBreaker
	bpCB     *circuitBreaker

	tagIdx tagIndex

	events EventEmitter

	clock clock.Clock

	closeOnce sync.Once
	done      chan struct{} // closed by Close(); background goroutines select on this
}

// prefixedKey adds the prefix to a given key.
func (c *cache) prefixedKey(key string) string {
	return c.opts.KeyPrefix + key
}

// storeSafely stores a value under key in L1 and L2 and publishes a
// backplane notification, honouring the skip flags in opts.
func (c *cache) storeSafely(ctx context.Context, key string, value any, opts EntryOptions) {
	pk := c.prefixedKey(key)
	now := c.clock.Now()

	// compute expiry times
	logicalExpiry := now.Add(opts.Duration)
	physicalExpiry := computePhysicalExpiry(now, opts)

	entry := &cacheEntry{
		value:          value,
		logicalExpiry:  logicalExpiry,
		physicalExpiry: physicalExpiry,
		createdAt:      now,
		tags:           opts.Tags,
	}

	// L1 write
	if !opts.SkipL1Write {
		c.setL1Entry(pk, entry, opts.Size)
	}

	// L2 write
	if c.l2 != nil && !opts.SkipL2Write {
		c.writeL2(ctx, key, value, opts)
	}

	// backplane notification
	if c.bp != nil && !opts.SkipBackplaneNotifications {
		c.publishBackplane(ctx, backplane.MessageTypeSet, key, opts)
	}
}

// computePhysicalExpiry returns when an entry written at now is physically
// removed: after FailSafeMaxDuration when fail-safe extends it, otherwise
// after Duration.
func computePhysicalExpiry(now time.Time, opts EntryOptions) time.Time {
	if opts.IsFailSafeEnabled && opts.FailSafeMaxDuration > opts.Duration {
		return now.Add(opts.FailSafeMaxDuration)
	}
	return now.Add(opts.Duration)
}

// l1Entry returns the L1 entry stored under the prefixed key pk.
func (c *cache) l1Entry(pk string) (*cacheEntry, bool) {
	raw, ok := c.l1.Get(pk)
	if !ok {
		return nil, false
	}
	entry, ok := raw.(*cacheEntry)
	return entry, ok
}

// setL1Entry stores entry in L1 under pk and indexes its tags.
func (c *cache) setL1Entry(pk string, entry *cacheEntry, size int64) {
	c.l1.Set(pk, entry, size)
	if len(entry.tags) > 0 {
		c.tagIdx.add(pk, entry.tags)
	}
}

// evictL1 removes pk from L1 and drops it from the tag index.
func (c *cache) evictL1(pk string) {
	raw, loaded := c.l1.LoadAndDelete(pk)
	if !loaded {
		return
	}
	if entry, ok := raw.(*cacheEntry); ok && len(entry.tags) > 0 {
		c.tagIdx.remove(pk, entry.tags)
	}
}

// expireL1 replaces the L1 entry under pk with a copy that is logically
// expired now, keeping it available as a fail-safe fallback.
func (c *cache) expireL1(pk string) {
	old, ok := c.l1Entry(pk)
	if !ok {
		return
	}
	expired := &cacheEntry{
		value:          old.value,
		logicalExpiry:  c.clock.Now(),
		physicalExpiry: old.physicalExpiry,
		createdAt:      old.createdAt,
		tags:           old.tags,
	}
	c.l1.CompareAndSwap(pk, old, expired)
}

// maybeAutoClone prevents callers from modifying cached pointers directly
// by serializing and deserializing the return value to clone it.
// Uses reflection to allocate a fresh value of the correct concrete type,
// ensuring pointer types get a true deep copy rather than a shallow copy.
func (c *cache) maybeAutoClone(value any, opts EntryOptions) (any, error) {
	if !opts.EnableAutoClone || c.serializer == nil || value == nil {
		return value, nil
	}

	data, err := c.serializer.Marshal(value)
	if err != nil {
		return value, err
	}

	// Allocate a new value of the same concrete type via reflection.
	t := reflect.TypeOf(value)
	var target reflect.Value
	if t.Kind() == reflect.Pointer {
		target = reflect.New(t.Elem()) // *T → new(T), returns *T
	} else {
		target = reflect.New(t) // T → new(T), returns *T
	}
	if err := c.serializer.Unmarshal(data, target.Interface()); err != nil {
		return value, err
	}
	if t.Kind() == reflect.Pointer {
		return target.Interface(), nil
	}
	return target.Elem().Interface(), nil
}

// GetOrSet returns the cached value for key. On a cache miss it calls
// factory, stores the result, and returns it. This is the primary method.
//
// The full execution path:
//
//  1. Read L1 (unless SkipL1Read)
//  2. On L1 miss, enter stampede protection (singleflight)
//  3. Re-check L1 inside the lock
//  4. Read L2 if configured (unless SkipL2Read or circuit breaker open)
//  5. Call factory with fail-safe and timeout handling
//  6. Store result in L1, L2, and notify backplane
//
// On a fresh L1 hit with EagerRefreshThreshold configured, a background
// refresh may be started.
func (c *cache) GetOrSet(
	ctx context.Context,
	key string,
	factory FactoryFunc,
	opts ...EntryOption,
) (any, error) {
	eo := applyOptions(c.opts.DefaultEntryOptions, opts)
	pk := c.prefixedKey(key)
	now := c.clock.Now()

	// L1 read
	var staleEntry *cacheEntry
	if !eo.SkipL1Read {
		entry, ok := c.l1Entry(pk)
		if ok && !entry.IsLogicallyExpired(now) {
			// fresh hit
			c.events.emit(EventCacheHit{Key: key})
			c.maybeStartEagerRefresh(ctx, key, entry, eo, factory)
			return c.maybeAutoClone(entry.value, eo)
		}
		// stale hit (or miss), keep as fail safe fallback
		staleEntry = entry
	}
	c.events.emit(EventCacheMiss{Key: key})

	doFn := func() (any, error) {
		return c.loadOrCreate(ctx, key, &eo, staleEntry, factory)
	}

	// execute with stampede protection
	var result any
	var err error
	if eo.LockTimeout > 0 {
		result, err, _ = c.stampede.DoWithTimeout(pk, eo.LockTimeout, doFn)
	} else {
		result, err, _ = c.stampede.Do(pk, doFn)
	}
	if err != nil {
		return nil, err
	}
	return c.maybeAutoClone(result, eo)
}

// loadOrCreate is the stampede-protected miss path of GetOrSet: it
// re-checks L1, then reads L2, then calls the factory. eo is shared with
// the factory via [FactoryExecutionContext.Options], so mutations made by
// the factory are visible to the caller.
func (c *cache) loadOrCreate(
	ctx context.Context,
	key string,
	eo *EntryOptions,
	staleEntry *cacheEntry,
	factory FactoryFunc,
) (any, error) {
	pk := c.prefixedKey(key)
	now := c.clock.Now()

	// recheck L1 in case another goroutine populated it while we waited
	if !eo.SkipL1Read {
		if entry, ok := c.l1Entry(pk); ok && !entry.IsLogicallyExpired(now) {
			return entry.value, nil
		}
	}

	// L2 read
	l2Value, l2Stale, l2Hit := c.loadFromL2(ctx, key, now, *eo)
	if l2Hit {
		return l2Value, nil
	}
	if staleEntry == nil {
		staleEntry = l2Stale
	}

	return c.callFactory(ctx, key, eo, staleEntry, factory)
}

// callFactory runs factory with fail-safe and timeout handling and stores
// a successful result.
func (c *cache) callFactory(
	ctx context.Context,
	key string,
	eo *EntryOptions,
	staleEntry *cacheEntry,
	factory FactoryFunc,
) (any, error) {
	c.events.emit(EventFactoryCall{Key: key})
	// Build the adaptive caching context. fctx.Options points at eo,
	// so factory mutations to fctx.Options.Duration (etc.) directly
	// modify eo which storeSafely reads after the factory returns.
	var staleVal any
	hasStale := staleEntry != nil
	if hasStale {
		staleVal = staleEntry.value
	}
	call := &factoryCall{
		key:        key,
		staleEntry: staleEntry,
		opts:       *eo,
		factory:    factory,
		fctx: &FactoryExecutionContext{
			Options:       eo,
			StaleValue:    staleVal,
			HasStaleValue: hasStale,
		},
	}

	newValue, err := c.executeWithFailSafe(ctx, call)
	if err != nil {
		c.events.emit(EventFactoryError{Key: key, Err: err})
		return nil, err
	}

	c.events.emit(EventFactorySuccess{Key: key})
	c.storeSafely(ctx, key, newValue, *eo)
	return newValue, nil
}

// Get returns the cached value for key if present and not logically
// expired. Returns (nil, false, nil) on a clean cache miss.
// When AllowStaleOnReadOnly is set, logically expired values are returned
// with ok == true. The event emitted in that case is [EventCacheHit] with
// IsStale: true.
//
// Reads L1 first, then L2 if configured and L1 missed.
func (c *cache) Get(ctx context.Context, key string, opts ...EntryOption) (any, bool, error) {
	eo := applyOptions(c.opts.DefaultEntryOptions, opts)
	pk := c.prefixedKey(key)
	now := c.clock.Now()

	// L1 read
	if !eo.SkipL1Read {
		entry, ok := c.l1Entry(pk)
		if ok && (!entry.IsLogicallyExpired(now) || eo.AllowStaleOnReadOnly) {
			c.events.emit(EventCacheHit{Key: key, IsStale: entry.IsLogicallyExpired(now)})
			val, err := c.maybeAutoClone(entry.value, eo)
			return val, true, err
		}
	}

	// L2 read
	if val, ok, err := c.getFromL2(ctx, key, now, eo); ok {
		return val, true, err
	}

	c.events.emit(EventCacheMiss{Key: key})
	return nil, false, nil
}

// Set stores value under key in all configured cache layers (L1, L2) and
// publishes a backplane notification. Skip flags in [EntryOptions] control
// which layers are written.
func (c *cache) Set(ctx context.Context, key string, value any, opts ...EntryOption) error {
	eo := applyOptions(c.opts.DefaultEntryOptions, opts)
	c.storeSafely(ctx, key, value, eo)
	return nil
}

// Delete removes the entry from L1 and L2. Publishes a
// [backplane.MessageTypeDelete] backplane notification.
// Does not error if the key does not exist.
func (c *cache) Delete(ctx context.Context, key string, opts ...EntryOption) error {
	eo := applyOptions(c.opts.DefaultEntryOptions, opts)
	pk := c.prefixedKey(key)

	// remove from l1
	c.evictL1(pk)

	// remove from l2
	if c.l2 != nil && !eo.SkipL2Write {
		if err := c.l2.Delete(ctx, pk); err != nil {
			c.logger.Warn("cache: L2 delete failed", slog.String("key", key), slog.String("error", err.Error()))
		}
	}

	// notify backplane
	if c.bp != nil && !eo.SkipBackplaneNotifications {
		c.publishBackplane(ctx, backplane.MessageTypeDelete, key, eo)
	}
	return nil
}

// Expire marks the entry as logically expired without removing it from L1.
// The value remains available as a fail-safe fallback until physical
// expiry. If fail-safe is not enabled on subsequent reads, the stale value
// is not returned. Publishes a [backplane.MessageTypeExpire] backplane
// notification.
func (c *cache) Expire(ctx context.Context, key string, opts ...EntryOption) error {
	eo := applyOptions(c.opts.DefaultEntryOptions, opts)
	c.expireL1(c.prefixedKey(key))

	// Notify backplane
	if c.bp != nil && !eo.SkipBackplaneNotifications {
		c.publishBackplane(ctx, backplane.MessageTypeExpire, key, eo)
	}

	return nil
}

// DeleteByTag removes all entries associated with tag from L1 (via the
// in-memory tag index) and L2 (via [l2.Adapter.DeleteByTag]). Publishes a
// [backplane.MessageTypeDelete] backplane notification for each removed
// key.
func (c *cache) DeleteByTag(ctx context.Context, tag string, opts ...EntryOption) error {
	eo := applyOptions(c.opts.DefaultEntryOptions, opts)

	// Get all L1 keys with this tag
	keys := c.tagIdx.keysForTag(tag)

	// Remove from L1
	for _, pk := range keys {
		c.evictL1(pk)
	}

	// Remove from L2 (adapter handles its own tag bookkeeping)
	if c.l2 != nil && !eo.SkipL2Write {
		if err := c.l2.DeleteByTag(ctx, tag); err != nil {
			c.logger.Warn("cache: L2 DeleteByTag failed",
				slog.String("tag", tag),
				slog.String("error", err.Error()),
			)
		}
	}

	// Notify backplane for each removed key
	if c.bp != nil && !eo.SkipBackplaneNotifications {
		for _, pk := range keys {
			unprefixed := pk[len(c.opts.KeyPrefix):]
			c.publishBackplane(ctx, backplane.MessageTypeDelete, unprefixed, eo)
		}
	}

	return nil
}

// Clear removes all entries from L1 and clears the tag index. When clearL2
// is true and an L2 adapter is configured, calls [l2.Adapter.Clear].
// Publishes a [backplane.MessageTypeClear] backplane notification.
func (c *cache) Clear(ctx context.Context, clearL2 bool) error { //nolint:revive // flag-parameter: public API mirrors IFusionCache
	// Clear L1: iterate and delete all entries
	c.l1.Clear()

	// Clear tag index
	c.tagIdx.clear()

	// Clear L2 if requested
	if clearL2 && c.l2 != nil {
		if err := c.l2.Clear(ctx); err != nil {
			c.logger.Warn("cache: L2 clear failed",
				slog.String("error", err.Error()),
			)
		}
	}

	// Notify backplane
	if c.bp != nil {
		c.publishBackplane(ctx, backplane.MessageTypeClear, "", applyOptions(c.opts.DefaultEntryOptions, nil))
	}

	return nil
}

// Close shuts down background goroutines (backplane subscription) and
// releases resources. Calls Close on the backplane and L1 adapter. Safe to
// call multiple times.
func (c *cache) Close() error {
	c.closeOnce.Do(func() {
		close(c.done)

		if c.bpCancelSub != nil {
			c.bpCancelSub()
		}
		if c.bp != nil {
			_ = c.bp.Close()
		}
		_ = c.l1.Close()
	})
	return nil
}

// Name returns the configured cache name. Defaults to "default".
func (c *cache) Name() string {
	return c.opts.CacheName
}

// DefaultEntryOptions returns a copy of the cache-wide default entry
// options. Safe to read and modify without affecting the cache.
func (c *cache) DefaultEntryOptions() EntryOptions {
	return c.opts.DefaultEntryOptions // value copy, safe to return
}

// Events returns the [EventEmitter] for subscribing to cache lifecycle
// events.
func (c *cache) Events() *EventEmitter {
	return &c.events
}
