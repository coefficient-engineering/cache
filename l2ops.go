package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"
)

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

// readL2 reads key from the L2 distributed cache and returns the decoded
// value and entry. A miss yields a nil entry with ok == true. ok is false
// when the read failed: an L2 error (reported via [EventL2Error]) or a
// deserialization error with ReThrowSerializationExceptions set (logged).
func (c *cache) readL2(ctx context.Context, key string, opts EntryOptions) (value any, entry *cacheEntry, ok bool) {
	if c.l2CB.IsOpen() {
		return nil, nil, true // circuit breaker open; treat as miss
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
		return nil, nil, false
	}
	if data == nil {
		return nil, nil, true // l2 miss
	}
	value, entry, err = c.unmarshalL2Entry(data)
	if err != nil {
		c.logger.Warn("cache: L2 deserialization failed",
			slog.String("key", pk),
			slog.String("error", err.Error()),
		)
		// without rethrow, a deserialization failure is treated as a miss
		return nil, nil, !opts.ReThrowSerializationExceptions
	}
	return value, entry, true
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
		PE:   computePhysicalExpiry(now, opts).UnixMilli(),
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

// getFromL2 reads key from L2 for Get. L2 read failures are reported by
// readL2 and treated as a miss. A fresh entry, or a stale one when
// AllowStaleOnReadOnly is set, is promoted to L1 and returned with
// ok == true.
func (c *cache) getFromL2(
	ctx context.Context,
	key string,
	now time.Time,
	eo EntryOptions,
) (value any, ok bool, err error) {
	if c.l2 == nil || eo.SkipL2Read || c.l2CB.IsOpen() {
		return nil, false, nil
	}
	l2Value, l2Entry, _ := c.readL2(ctx, key, eo)
	if l2Entry == nil {
		return nil, false, nil
	}
	fresh := !l2Entry.IsLogicallyExpired(now)
	if !fresh && !eo.AllowStaleOnReadOnly {
		return nil, false, nil
	}
	if fresh {
		c.events.emit(EventL2Hit{Key: key})
	}
	if !eo.SkipL1Write {
		c.setL1Entry(c.prefixedKey(key), l2Entry, eo.Size)
	}
	value, err = c.maybeAutoClone(l2Value, eo)
	return value, true, err
}

// loadFromL2 reads key from L2 for GetOrSet. A fresh hit is promoted to L1
// and returned with hit == true. Otherwise stale holds a logically expired
// L2 entry usable as a fail-safe fallback, or nil.
func (c *cache) loadFromL2(
	ctx context.Context,
	key string,
	now time.Time,
	eo EntryOptions,
) (value any, stale *cacheEntry, hit bool) {
	if c.l2 == nil || eo.SkipL2Read || c.l2CB.IsOpen() {
		return nil, nil, false
	}
	l2Value, l2Entry, readOK := c.readL2(ctx, key, eo)
	if !readOK {
		return nil, nil, false // readL2 already reported the failure
	}
	if l2Entry == nil {
		c.events.emit(EventL2Miss{Key: key})
		return nil, nil, false
	}
	if l2Entry.IsLogicallyExpired(now) {
		// stale l2, use as fail safe candidate
		c.events.emit(EventL2Miss{Key: key})
		return nil, l2Entry, false
	}
	// fresh l2 hit, promote to l1
	c.events.emit(EventL2Hit{Key: key})
	if !eo.SkipL1Write {
		c.setL1Entry(c.prefixedKey(key), l2Entry, eo.Size)
	}
	return l2Value, nil, true
}
