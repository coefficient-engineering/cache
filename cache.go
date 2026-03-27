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
	"github.com/coefficient-engineering/cache/l2"
	"github.com/coefficient-engineering/cache/serializer"
)

type Cache interface {
	// Get returns the cached value for key if present and not logically expired.
	// Returns (nil, false, nil) on a clean cache miss.
	Get(ctx context.Context, key string, opts ...EntryOption) (any, bool, error)

	// Set stores value under key in all configured cache layers.
	Set(ctx context.Context, key string, value any, opts ...EntryOption) error

	// GetOrSet returns the cached value for key. On a cache miss it calls factory,
	// stores the result, and returns it. This is the primary method.
	GetOrSet(ctx context.Context, key string, factory FactoryFunc, opts ...EntryOption) (any, error)

	// Delete removes the entry from all cache layers and notifies the backplane.
	Delete(ctx context.Context, key string, opts ...EntryOption) error

	// DeleteByTag removes all entries associated with tag from all layers.
	DeleteByTag(ctx context.Context, tag string, opts ...EntryOption) error

	// Expire marks the entry as logically expired without removing it.
	// The value remains available as a fail-safe fallback until physical expiry.
	// If fail-safe is not enabled, this is equivalent to Delete.
	Expire(ctx context.Context, key string, opts ...EntryOption) error

	// Clear removes all entries from L1. If clearL2 is true and an L2 adapter
	// is configured, it calls Clear on the adapter as well.
	Clear(ctx context.Context, clearL2 bool) error

	// Name returns the configured cache name.
	Name() string

	// DefaultEntryOptions returns a copy of the cache-wide default entry options.
	DefaultEntryOptions() EntryOptions

	// Events returns the EventEmitter for subscribing to cache lifecycle events.
	Events() *EventEmitter

	// Close shuts down background goroutines and releases resources.
	// It is safe to call Close multiple times.
	Close() error
}

// FactoryFunc is the function called on a cache miss to produce a fresh value.
// It receives the request context and should respect cancellation.
type FactoryFunc func(ctx context.Context) (any, error)

func GetOrSet[T any](
	ctx context.Context,
	c Cache,
	key string,
	factory func(ctx context.Context) (T, error),
	opts ...EntryOption,
) (T, error) {
	var zero T
	raw, err := c.GetOrSet(ctx, key, func(ctx context.Context) (any, error) {
		return factory(ctx)
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

// cache is the concrete implementation of the Cache interface.
// created via New()
type cache struct {
	// config
	opts   Options // full cache-wide config snapshot
	logger *slog.Logger

	l1 sync.Map

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
		c.l1.Store(pk, entry)
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
		if raw, ok := c.l1.Load(pk); ok {
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
			if raw, ok := c.l1.Load(pk); ok {
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
						c.l1.Store(pk, l2Entry)
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
		newValue, err := c.executeWithFailSafe(ctx, key, staleEntry, eo, factory)
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

func (c *cache) Get(ctx context.Context, key string, opts ...EntryOption) (any, bool, error) {
	eo := applyOptions(c.opts.DefaultEntryOptions, opts)
	pk := c.prefixedKey(key)
	now := c.clock.Now()

	// L1 read
	if !eo.SkipL1Read {
		if raw, ok := c.l1.Load(pk); ok {
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
					c.l1.Store(pk, l2Entry)
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

func (c *cache) Set(ctx context.Context, key string, value any, opts ...EntryOption) error {
	eo := applyOptions(c.opts.DefaultEntryOptions, opts)
	c.storeSafely(ctx, key, value, eo)
	return nil
}

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

func (c *cache) Expire(ctx context.Context, key string, opts ...EntryOption) error {
	eo := applyOptions(c.opts.DefaultEntryOptions, opts)
	pk := c.prefixedKey(key)

	if raw, ok := c.l1.Load(pk); ok {
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

func (c *cache) Clear(ctx context.Context, clearL2 bool) error {
	// Clear L1: iterate and delete all entries
	c.l1.Range(func(key, value any) bool {
		c.l1.Delete(key)
		return true
	})

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

func (c *cache) Close() error {
	c.closeOnce.Do(func() {
		close(c.done)

		if c.bpCancelSub != nil {
			c.bpCancelSub()
		}
		if c.bp != nil {
			_ = c.bp.Close()
		}
	})
	return nil
}

func (c *cache) Name() string {
	return c.opts.CacheName
}

func (c *cache) DefaultEntryOptions() EntryOptions {
	return c.opts.DefaultEntryOptions // value copy, safe to return
}

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
		if raw, ok := c.l1.Load(pk); ok {
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
		c.l1.Range(func(key, value any) bool {
			c.l1.Delete(key)
			return true
		})
		c.tagIdx.clear()
	}
}

func (c *cache) attemptAutoRecovery() {
	if !c.opts.BackplaneAutoRecovery {
		return
	}
	c.logger.Info("cache: backplane auto-recovery, clearing L1...")
	c.l1.Range(func(key, value any) bool {
		c.l1.Delete(key)
		return true
	})
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
