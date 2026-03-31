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
	"encoding/json"
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
type Cache interface {
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

// storeSafely stores a value to a key onto both the L1 and the L2 caches, as well as
// updating the backplane when enabled.
func (c *cache) storeSafely(ctx context.Context, key string, value any, opts EntryOptions) {
	pk := c.prefixedKey(key)
	now := c.clock.Now()

	// compute expiry times
	logicalExpiry := now.Add(opts.Duration)
	physicalExpiry := c.computePhysicalExpiry(now, opts)

	entry := &cacheEntry{
		value:          value,
		logicalExpiry:  logicalExpiry,
		physicalExpiry: physicalExpiry,
		createdAt:      now,
		tags:           opts.Tags,
	}

	// L1 write
	if !opts.SkipL1Write {
		c.l1.Set(pk, entry, opts.Size)
		if len(opts.Tags) > 0 {
			c.tagIdx.add(pk, opts.Tags)
		}
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

func (c *cache) computePhysicalExpiry(now time.Time, opts EntryOptions) (physicalExpiry time.Time) {
	if opts.IsFailSafeEnabled && opts.FailSafeMaxDuration > opts.Duration {
		return now.Add(opts.FailSafeMaxDuration)
	}
	return now.Add(opts.Duration)
}

// maybeAutoClone prevents callers from modifying cached pointers directly
// by serializing and deserializing the return value to clone it.
// Uses reflection to allocate a fresh value of the correct concrete type,
// ensuring pointer types get a true deep copy rather than a shallow pointer copy.
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
	if t.Kind() == reflect.Ptr {
		target = reflect.New(t.Elem()) // *T → new(T), returns *T
	} else {
		target = reflect.New(t) // T → new(T), returns *T
	}
	if err := c.serializer.Unmarshal(data, target.Interface()); err != nil {
		return value, err
	}
	if t.Kind() == reflect.Ptr {
		return target.Interface(), nil
	}
	return target.Elem().Interface(), nil
}

// Returns the cached value for `key`. On a cache miss, calls `factory`, stores the result, and returns it.
// This is the primary method.
//
// The full execution path:
//
// 1. Read L1 (unless `SkipL1Read`)
// 2. On L1 miss, enter stampede protection (singleflight)
// 3. Re-check L1 inside the lock
// 4. Read L2 if configured (unless `SkipL2Read` or circuit breaker open)
// 5. Call `factory` with fail-safe and timeout handling
// 6. Store result in L1, L2, and notify backplane
//
// On a fresh L1 hit with `EagerRefreshThreshold` configured, a background refresh may be started.
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
		if raw, ok := c.l1.Get(pk); ok {
			entry := raw.(*cacheEntry)
			if !entry.IsLogicallyExpired(now) {
				// fresh hit
				c.events.emit(EventCacheHit{Key: key})
				c.maybeStartEagerRefresh(ctx, key, entry, eo, factory)
				val, err := c.maybeAutoClone(entry.value, eo)
				return val, err
			}
			// stale hit, keep as fail safe fallback
			staleEntry = entry
		}
	}
	c.events.emit(EventCacheMiss{Key: key})

	doFn := func() (any, error) {
		now := c.clock.Now()

		// recheck L1 in case another goroutine populated it while we waited
		if !eo.SkipL1Read {
			if raw, ok := c.l1.Get(pk); ok {
				entry := raw.(*cacheEntry)
				if !entry.IsLogicallyExpired(now) {
					return entry.value, nil
				}
			}
		}

		// L2 read
		if c.l2 != nil && !eo.SkipL2Read && !c.l2CB.IsOpen() {
			l2Value, l2Entry, err := c.readL2(ctx, key, eo)
			if err != nil {
				// readL2 emits EventL2Error internally
			} else if l2Entry != nil {
				if !l2Entry.IsLogicallyExpired(now) {
					// fresh l2 hit, promote to l1
					c.events.emit(EventL2Hit{Key: key})
					if !eo.SkipL1Write {
						c.l1.Set(pk, l2Entry, eo.Size)
						if len(l2Entry.tags) > 0 {
							c.tagIdx.add(pk, l2Entry.tags)
						}
					}
					return l2Value, nil
				}
				// stale l2, use fail safe candidate
				if staleEntry == nil {
					staleEntry = l2Entry
				}
				c.events.emit(EventL2Miss{Key: key})
			} else {
				c.events.emit(EventL2Miss{Key: key})
			}
		}

		// factory call
		c.events.emit(EventFactoryCall{Key: key})
		// Build the adaptive caching context. fctx.Options points at eo,
		// so factory mutations to fctx.Options.Duration (etc.) directly
		// modify eo which storeSafely reads after the factory returns.
		var staleVal any
		hasStale := staleEntry != nil
		if hasStale {
			staleVal = staleEntry.value
		}
		fctx := &FactoryExecutionContext{
			Options:       &eo,
			StaleValue:    staleVal,
			HasStaleValue: hasStale,
		}

		newValue, err := c.executeWithFailSafe(ctx, key, staleEntry, eo, factory, fctx)
		if err != nil {
			c.events.emit(EventFactoryError{Key: key, Err: err})
			return nil, err
		}

		c.events.emit(EventFactorySuccess{Key: key})
		c.storeSafely(ctx, key, newValue, eo)
		return newValue, nil
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

// Get returns the cached value for `key` if present and not logically expired. Returns `(nil, false, nil)` on a clean cache miss.
// When `AllowStaleOnReadOnly` is set, logically expired values are returned with `ok == true`.
// The event emitted in that case is `EventCacheHit` with `IsStale: true`.
//
// Reads L1 first, then L2 if configured and L1 missed.
func (c *cache) Get(ctx context.Context, key string, opts ...EntryOption) (any, bool, error) {
	eo := applyOptions(c.opts.DefaultEntryOptions, opts)
	pk := c.prefixedKey(key)
	now := c.clock.Now()

	// L1 read
	if !eo.SkipL1Read {
		if raw, ok := c.l1.Get(pk); ok {
			entry := raw.(*cacheEntry)
			if !entry.IsLogicallyExpired(now) {
				c.events.emit(EventCacheHit{Key: key})
				val, err := c.maybeAutoClone(entry.value, eo)
				return val, true, err
			}
			// stale entry
			if eo.AllowStaleOnReadOnly {
				c.events.emit(EventCacheHit{Key: key, IsStale: true})
				val, err := c.maybeAutoClone(entry.value, eo)
				return val, true, err
			}
		}
	}

	// L2 read
	if c.l2 != nil && !eo.SkipL2Read && !c.l2CB.IsOpen() {
		l2Value, l2Entry, err := c.readL2(ctx, key, eo)
		if err != nil {
			// readL2 emits EventL2Error internally
		} else if l2Entry != nil {
			if !l2Entry.IsLogicallyExpired(now) || eo.AllowStaleOnReadOnly {
				if !l2Entry.IsLogicallyExpired(now) {
					c.events.emit(EventL2Hit{Key: key})
				}
				if !eo.SkipL1Write {
					c.l1.Set(pk, l2Entry, eo.Size)
					if len(l2Entry.tags) > 0 {
						c.tagIdx.add(pk, l2Entry.tags)
					}
				}
				val, err := c.maybeAutoClone(l2Value, eo)
				return val, true, err
			}
		}
	}

	c.events.emit(EventCacheMiss{Key: key})
	return nil, false, nil
}

// Set stores `value` under `key` in all configured cache layers (L1, L2) and publishes
// a backplane notification. Skip flags in `EntryOptions` control which layers are written.
func (c *cache) Set(ctx context.Context, key string, value any, opts ...EntryOption) error {
	eo := applyOptions(c.opts.DefaultEntryOptions, opts)
	c.storeSafely(ctx, key, value, eo)
	return nil
}

// Removes the entry from L1 and L2. Publishes a `MessageTypeDelete`
// backplane notification.
// Does not error if the key does not exist.
func (c *cache) Delete(ctx context.Context, key string, opts ...EntryOption) error {
	eo := applyOptions(c.opts.DefaultEntryOptions, opts)
	pk := c.prefixedKey(key)

	// remove from l1
	if raw, loaded := c.l1.LoadAndDelete(pk); loaded {
		entry := raw.(*cacheEntry)
		if len(entry.tags) > 0 {
			c.tagIdx.remove(pk, entry.tags)
		}
	}

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

// Marks the entry as logically expired without removing it from L1.
// The value remains available as a fail-safe fallback until physical expiry.
// If fail-safe is not enabled on subsequent reads, the stale value is not returned.
// Publishes a `MessageTypeExpire` backplane notification.
func (c *cache) Expire(ctx context.Context, key string, opts ...EntryOption) error {
	eo := applyOptions(c.opts.DefaultEntryOptions, opts)
	pk := c.prefixedKey(key)

	if raw, ok := c.l1.Get(pk); ok {
		old := raw.(*cacheEntry)
		expired := &cacheEntry{
			value:          old.value,
			logicalExpiry:  c.clock.Now(), // expired now
			physicalExpiry: old.physicalExpiry,
			createdAt:      old.createdAt,
			tags:           old.tags,
		}
		c.l1.CompareAndSwap(pk, raw, expired)
	}

	// Notify backplane
	if c.bp != nil && !eo.SkipBackplaneNotifications {
		c.publishBackplane(ctx, backplane.MessageTypeExpire, key, eo)
	}

	return nil
}

// Removes all entries associated with `tag` from L1 (via the in-memory tag index) and
// L2 (via `L2Adapter.DeleteByTag`).
// Publishes a `MessageTypeDelete` backplane notification for each removed key.
func (c *cache) DeleteByTag(ctx context.Context, tag string, opts ...EntryOption) error {
	eo := applyOptions(c.opts.DefaultEntryOptions, opts)

	// Get all L1 keys with this tag
	keys := c.tagIdx.keysForTag(tag)

	// Remove from L1
	for _, pk := range keys {
		if raw, loaded := c.l1.LoadAndDelete(pk); loaded {
			entry := raw.(*cacheEntry)
			c.tagIdx.remove(pk, entry.tags)
		}
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

// Removes all entries from L1 and clears the tag index. When `clearL2` is true and
// an L2 adapter is configured, calls `L2Adapter.Clear`.
// Publishes a `MessageTypeClear` backplane notification.
func (c *cache) Clear(ctx context.Context, clearL2 bool) error {
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

// Shuts down background goroutines (backplane subscription) and releases resources.
// Calls `Close` on the backplane and L1 adapter. Safe to call multiple times.
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

// Returns the configured cache name. Defaults to `"default"`.
func (c *cache) Name() string {
	return c.opts.CacheName
}

// Returns a copy of the cache-wide default entry options.
// Safe to read and modify without affecting the cache.
func (c *cache) DefaultEntryOptions() EntryOptions {
	return c.opts.DefaultEntryOptions // value copy, safe to return
}

// Returns the `EventEmitter` for subscribing to cache lifecycle events.
func (c *cache) Events() *EventEmitter {
	return &c.events
}

// writeL2 writes a value to the L2 distributed cache.
func (c *cache) writeL2(ctx context.Context, key string, value any, opts EntryOptions) {
	if c.l2CB.IsOpen() {
		return // circuit breaker open; skip l2
	}
	pk := c.prefixedKey(key)
	doWrite := func() {
		data, err := c.marshalL2Entry(value, opts)
		if err != nil {
			if opts.ReThrowSerializationExceptions {
				c.logger.Error("cache: L2 serialization failed",
					slog.String("key", pk),
					slog.String("error", err.Error()),
				)
			}
			return
		}
		ttl := effectiveTTL(opts)
		writeCtx := ctx
		if opts.DistributedCacheHardTimeout > 0 {
			var cancel context.CancelFunc
			writeCtx, cancel = context.WithTimeout(ctx, opts.DistributedCacheHardTimeout)
			defer cancel()
		}
		err = c.l2.Set(writeCtx, pk, data, ttl, opts.Tags)
		c.l2CB.Record(err)
		if err != nil {
			c.events.emit(EventL2Error{Key: key, Err: err})
			c.logger.Warn("cache: L2 write failed",
				slog.String("key", pk),
				slog.String("error", err.Error()),
			)
		}
	}
	if opts.AllowBackgroundDistributedCacheOperations {
		go doWrite()
	} else {
		doWrite()
	}
}

// readL2 reads from the L2 distributed cache and returns the value and entry.
func (c *cache) readL2(ctx context.Context, key string, opts EntryOptions) (any, *cacheEntry, error) {
	if c.l2CB.IsOpen() {
		return nil, nil, nil // circuit breaker open; treat as miss
	}
	pk := c.prefixedKey(key)
	// apply l2 timeout
	readCtx := ctx
	if opts.DistributedCacheHardTimeout > 0 {
		var cancel context.CancelFunc
		readCtx, cancel = context.WithTimeout(ctx, opts.DistributedCacheHardTimeout)
		defer cancel()
	}
	data, err := c.l2.Get(readCtx, pk)
	c.l2CB.Record(err)
	if err != nil {
		c.events.emit(EventL2Error{Key: key, Err: err})
		return nil, nil, err
	}
	if data == nil {
		return nil, nil, nil // l2 miss
	}
	value, entry, err := c.unmarshalL2Entry(data)
	if err != nil {
		c.logger.Warn("cache: L2 deserialization failed",
			slog.String("key", pk),
			slog.String("error", err.Error()),
		)
		if opts.ReThrowSerializationExceptions {
			return nil, nil, err
		}
		return nil, nil, nil // treat deserialization failure as miss
	}
	return value, entry, nil
}

// publishBackplane publishes a message to the backplane.
func (c *cache) publishBackplane(ctx context.Context, msgType backplane.MessageType, key string, opts EntryOptions) {
	if c.bp == nil || opts.SkipBackplaneNotifications {
		return
	}
	if c.bpCB.IsOpen() {
		return // circuit breaker open
	}
	msg := backplane.Message{
		Type:      msgType,
		CacheName: c.opts.CacheName,
		Key:       key,
		SourceID:  c.opts.NodeID,
	}

	doPublish := func() {
		err := c.bp.Publish(ctx, msg)
		c.bpCB.Record(err)
		if err != nil {
			c.logger.Warn("cache: backplane publish failed",
				slog.String("key", key),
				slog.String("type", fmt.Sprintf("%d", msgType)),
				slog.String("error", err.Error()),
			)
			if !opts.AllowBackgroundBackplaneOperations && opts.ReThrowBackplaneExceptions {
				// in sync mode with rethrow, the error would be returned.
				// but publishBackplane is void, errors are logged not returned.
				// the caller checks these flags in its own err handling
			}
		} else {
			c.events.emit(EventBackplaneSent{Key: key, Type: msgType})
		}
	}
	if opts.AllowBackgroundBackplaneOperations {
		go doPublish()
	} else {
		doPublish()
	}
}

// handleBackplaneMessage processes an incoming backplane message.
func (c *cache) handleBackplaneMessage(msg backplane.Message) {
	// only process messages for our cache name
	if msg.CacheName != c.opts.CacheName {
		return
	}
	// ignore msgs from ourselves
	if msg.SourceID == c.opts.NodeID {
		return
	}
	// user opt out
	if c.opts.IgnoreIncomingBackplaneNotifications {
		return
	}
	c.events.emit(EventBackplaneReceived{Key: msg.Key, Type: msg.Type})

	switch msg.Type {
	case backplane.MessageTypeSet:
		// another node wrote this key, evict from l1 so next read gets fresh from l2
		pk := c.prefixedKey(msg.Key)
		if raw, loaded := c.l1.LoadAndDelete(pk); loaded {
			entry := raw.(*cacheEntry)
			if len(entry.tags) > 0 {
				c.tagIdx.remove(pk, entry.tags)
			}
		}

	case backplane.MessageTypeDelete:
		pk := c.prefixedKey(msg.Key)
		if raw, loaded := c.l1.LoadAndDelete(pk); loaded {
			entry := raw.(*cacheEntry)
			if len(entry.tags) > 0 {
				c.tagIdx.remove(pk, entry.tags)
			}
		}

	case backplane.MessageTypeExpire:
		pk := c.prefixedKey(msg.Key)
		if raw, ok := c.l1.Get(pk); ok {
			old := raw.(*cacheEntry)
			expired := &cacheEntry{
				value:          old.value,
				logicalExpiry:  c.clock.Now(),
				physicalExpiry: old.physicalExpiry,
				createdAt:      old.createdAt,
				tags:           old.tags,
			}
			c.l1.CompareAndSwap(pk, raw, expired)
		}

	case backplane.MessageTypeClear:
		c.l1.Clear()
		c.tagIdx.clear()
	}
}

func (c *cache) attemptAutoRecovery() {
	if !c.opts.BackplaneAutoRecovery {
		return
	}
	c.logger.Info("cache: backplane auto-recovery, clearing L1...")
	c.l1.Clear()
	c.tagIdx.clear()
}

func (c *cache) marshalL2Entry(value any, opts EntryOptions) ([]byte, error) {
	// serialize user value with configured serializer
	innerBytes, err := c.serializer.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("cache: serialize value: %w", err)
	}
	now := c.clock.Now()

	return json.Marshal(l2Envelope{
		V:    innerBytes,
		LE:   now.Add(opts.Duration).UnixMilli(),
		PE:   c.computePhysicalExpiry(now, opts).UnixMilli(),
		Tags: opts.Tags,
	})
}

func (c *cache) unmarshalL2Entry(data []byte) (any, *cacheEntry, error) {
	// deserialize l2Envelope
	var env l2Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return nil, nil, fmt.Errorf("cache: deserialize envelope: %w", err)
	}
	// NOTE: Deserializing into `any` loses concrete type information with
	// JSON-based serializers (e.g., *MyStruct becomes map[string]interface{}).
	// L1-only paths store the original Go value directly and are unaffected.
	// For L2, use a type-aware serializer (e.g., gob) if concrete type
	// round-tripping is required.
	var value any
	if err := c.serializer.Unmarshal(env.V, &value); err != nil {
		return nil, nil, fmt.Errorf("cache: deserialize value: %w", err)
	}
	// reconstruct cache entry
	return value, &cacheEntry{
		value:          value,
		logicalExpiry:  time.UnixMilli(env.LE),
		physicalExpiry: time.UnixMilli(env.PE),
		createdAt:      time.UnixMilli(env.LE).Add(-1), // approx
		tags:           env.Tags,
	}, nil
}
