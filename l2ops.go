package cache

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"
)

// writeL2 serializes value into an L2 envelope and writes it to L2, in a
// goroutine when AllowBackgroundDistributedCacheOperations is set. Every
// failure is logged. The returned error is non-nil only for a synchronous
// write whose failure opts rethrows: a serialization error under
// ReThrowSerializationExceptions or an L2 error under
// ReThrowDistributedCacheExceptions.
func (c *cache) writeL2(ctx context.Context, key string, value any, opts EntryOptions) error {
	if c.l2CB.IsOpen() {
		return nil // circuit breaker open; skip l2
	}
	pk := c.prefixedKey(key)
	doWrite := func() error {
		data, err := c.marshalL2Entry(value, opts)
		if err != nil {
			c.logger.Error("cache: L2 serialization failed",
				slog.String("key", key),
				slog.String("error", err.Error()),
			)
			if opts.ReThrowSerializationExceptions {
				return fmt.Errorf("cache: L2 serialization (key %q): %w", key, err)
			}
			return nil
		}
		err = c.callL2(ctx, opts, func(ctx context.Context) error {
			return c.l2.Set(ctx, pk, data, effectiveTTL(opts), opts.Tags)
		})
		if err != nil {
			return c.l2Failed(ctx, opts, l2Op{name: "write", key: key}, err)
		}
		return nil
	}
	if opts.AllowBackgroundDistributedCacheOperations {
		go func() { _ = doWrite() }() // background: failures already logged
		return nil
	}
	return doWrite()
}

// getL2 reads pk from L2 under callL2's rules. With a positive
// softTimeout the read runs in a goroutine; if it has not finished after
// softTimeout, its context is cancelled and skipped is true. The abandoned
// goroutine exits as soon as the adapter honours the cancellation, and its
// outcome is not recorded in the circuit breaker.
func (c *cache) getL2(ctx context.Context, pk string, opts EntryOptions, softTimeout time.Duration) (data []byte, skipped bool, err error) {
	get := func(ctx context.Context) error {
		var getErr error
		data, getErr = c.l2.Get(ctx, pk)
		return getErr
	}
	if softTimeout <= 0 {
		return data, false, c.callL2(ctx, opts, get)
	}
	readCtx, cancel := l2Context(ctx, opts)
	defer cancel()
	type result struct {
		data []byte
		err  error
	}
	done := make(chan result, 1) // buffered: the goroutine never blocks
	go func() {
		d, getErr := c.l2.Get(readCtx, pk)
		done <- result{d, getErr}
	}()
	timer := time.NewTimer(softTimeout)
	defer timer.Stop()
	select {
	case r := <-done:
		c.l2CB.Record(r.err)
		return r.data, false, r.err
	case <-timer.C:
		return nil, true, nil // deferred cancel stops the read
	}
}

// l2Context derives the context for one L2 call, bounded by
// DistributedCacheHardTimeout when set.
func l2Context(ctx context.Context, opts EntryOptions) (context.Context, context.CancelFunc) {
	if opts.DistributedCacheHardTimeout > 0 {
		return context.WithTimeout(ctx, opts.DistributedCacheHardTimeout)
	}
	return context.WithCancel(ctx)
}

// callL2 runs one L2 adapter call bounded by DistributedCacheHardTimeout and
// records its outcome in the L2 circuit breaker.
func (c *cache) callL2(ctx context.Context, opts EntryOptions, call func(context.Context) error) error {
	ctx, cancel := l2Context(ctx, opts)
	defer cancel()
	err := call(ctx)
	c.l2CB.Record(err)
	return err
}

// l2Op describes an L2 operation in failure reports: its name and the
// caller's key or tag it targeted (both empty for clear).
type l2Op struct {
	name string
	key  string
	tag  string
}

// l2Failed reports a failed L2 operation via [EventL2Error] and a warning
// log. It returns err wrapped with the operation when opts rethrows L2
// errors ([EntryOptions.ReThrowDistributedCacheExceptions]), otherwise nil.
func (c *cache) l2Failed(ctx context.Context, opts EntryOptions, op l2Op, err error) error {
	c.events.emit(EventL2Error{Key: op.key, Err: err})
	c.logger.LogAttrs(ctx, slog.LevelWarn, "cache: L2 operation failed",
		slog.String("op", op.name),
		slog.String("key", op.key),
		slog.String("tag", op.tag),
		slog.String("error", err.Error()),
	)
	if !opts.ReThrowDistributedCacheExceptions {
		return nil
	}
	switch {
	case op.key != "":
		return fmt.Errorf("cache: L2 %s (key %q): %w", op.name, op.key, err)
	case op.tag != "":
		return fmt.Errorf("cache: L2 %s (tag %q): %w", op.name, op.tag, err)
	default:
		return fmt.Errorf("cache: L2 %s: %w", op.name, err)
	}
}

// readL2 reads key from L2 and decodes it. entry is nil on a miss, while
// the L2 circuit breaker is open, and after a swallowed failure; ok is
// false when the L2 call itself failed. err is non-nil only for a failure
// opts rethrows: an L2 error under ReThrowDistributedCacheExceptions or a
// deserialization error under ReThrowSerializationExceptions. Without
// rethrow, a deserialization failure is logged and treated as a miss.
// A positive softTimeout abandons a slower read: it is logged and reported
// like a failed read (ok false, no error), without touching the breaker.
func (c *cache) readL2(
	ctx context.Context,
	key string,
	opts EntryOptions,
	softTimeout time.Duration,
) (value any, entry *cacheEntry, ok bool, err error) {
	if c.l2CB.IsOpen() {
		return nil, nil, true, nil // circuit breaker open; treat as miss
	}
	pk := c.prefixedKey(key)
	data, skipped, err := c.getL2(ctx, pk, opts, softTimeout)
	if skipped {
		c.logger.Warn("cache: L2 soft timeout, skipping L2 read",
			slog.String("key", key),
			slog.Duration("timeout", softTimeout),
		)
		return nil, nil, false, nil
	}
	if err != nil {
		return nil, nil, false, c.l2Failed(ctx, opts, l2Op{name: "read", key: key}, err)
	}
	if data == nil {
		return nil, nil, true, nil // l2 miss
	}
	value, entry, err = c.unmarshalL2Entry(data)
	if err != nil {
		c.logger.Warn("cache: L2 deserialization failed",
			slog.String("key", key),
			slog.String("error", err.Error()),
		)
		if opts.ReThrowSerializationExceptions {
			return nil, nil, true, fmt.Errorf("cache: L2 deserialization (key %q): %w", key, err)
		}
		return nil, nil, true, nil // treated as a miss
	}
	return value, entry, true, nil
}

func (c *cache) marshalL2Entry(value any, opts EntryOptions) ([]byte, error) {
	// serialize user value with configured serializer
	innerBytes, err := c.serializer.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("value: %w", err)
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
		return nil, nil, fmt.Errorf("envelope: %w", err)
	}
	// NOTE: Deserializing into `any` loses concrete type information with
	// JSON-based serializers (e.g., *MyStruct becomes map[string]interface{}).
	// L1-only paths store the original Go value directly and are unaffected.
	// For L2, use a type-aware serializer (e.g., gob) if concrete type
	// round-tripping is required.
	var value any
	if err := c.serializer.Unmarshal(env.V, &value); err != nil {
		return nil, nil, fmt.Errorf("value: %w", err)
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

// getFromL2 reads key from L2 for Get. A fresh entry, or a stale one when
// AllowStaleOnReadOnly is set, is promoted to L1 and returned with
// ok == true. err is a read failure rethrown by readL2; other failures
// are reported there and treated as a miss.
func (c *cache) getFromL2(
	ctx context.Context,
	key string,
	now time.Time,
	eo EntryOptions,
) (value any, ok bool, err error) {
	if c.l2 == nil || eo.SkipL2Read || c.l2CB.IsOpen() {
		return nil, false, nil
	}
	l2Value, l2Entry, _, err := c.readL2(ctx, key, eo, 0)
	if err != nil || l2Entry == nil {
		return nil, false, err
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
// L2 entry usable as a fail-safe fallback, or nil. err is a read failure
// rethrown by readL2; GetOrSet returns it without calling the factory.
// A positive softTimeout bounds the read (see getL2); GetOrSet passes
// DistributedCacheSoftTimeout only when a fail-safe stale value already
// exists, so an abandoned read goes straight to the factory with it.
func (c *cache) loadFromL2(
	ctx context.Context,
	key string,
	now time.Time,
	eo EntryOptions,
	softTimeout time.Duration,
) (value any, stale *cacheEntry, hit bool, err error) {
	if c.l2 == nil || eo.SkipL2Read || c.l2CB.IsOpen() {
		return nil, nil, false, nil
	}
	l2Value, l2Entry, readOK, err := c.readL2(ctx, key, eo, softTimeout)
	if err != nil || !readOK {
		return nil, nil, false, err // readL2 already reported the failure
	}
	if l2Entry == nil {
		c.events.emit(EventL2Miss{Key: key})
		return nil, nil, false, nil
	}
	if l2Entry.IsLogicallyExpired(now) {
		// stale l2, use as fail safe candidate
		c.events.emit(EventL2Miss{Key: key})
		return nil, l2Entry, false, nil
	}
	// fresh l2 hit, promote to l1
	c.events.emit(EventL2Hit{Key: key})
	if !eo.SkipL1Write {
		c.setL1Entry(c.prefixedKey(key), l2Entry, eo.Size)
	}
	return l2Value, nil, true, nil
}

// opExpire names Expire's L2 operation in failure reports.
const opExpire = "expire"

// expireL2 makes the L2 entry for key logically expired. With fail-safe
// off it deletes the entry. With fail-safe on it rewrites the envelope
// with logical expiry = now, keeping its value, tags and physical expiry
// (the TTL becomes the time left until physical expiry), so other nodes
// still have a fail-safe fallback. A missing or physically expired entry
// is left alone. Unlike Set, Expire is not gated by the L2 circuit
// breaker, so an invalidation is never silently skipped. Errors follow
// [EntryOptions.ReThrowDistributedCacheExceptions] and, for an
// undecodable envelope, [EntryOptions.ReThrowSerializationExceptions].
func (c *cache) expireL2(ctx context.Context, key string, opts EntryOptions) error {
	pk := c.prefixedKey(key)
	if !opts.IsFailSafeEnabled {
		if err := c.callL2(ctx, opts, func(ctx context.Context) error { return c.l2.Delete(ctx, pk) }); err != nil {
			return c.l2Failed(ctx, opts, l2Op{name: opExpire, key: key}, err)
		}
		return nil
	}
	var data []byte
	err := c.callL2(ctx, opts, func(ctx context.Context) error {
		var getErr error
		data, getErr = c.l2.Get(ctx, pk)
		return getErr
	})
	if err != nil {
		return c.l2Failed(ctx, opts, l2Op{name: opExpire, key: key}, err)
	}
	if data == nil {
		return nil
	}
	var env l2Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		c.logger.Warn("cache: L2 deserialization failed",
			slog.String("key", key),
			slog.String("error", err.Error()),
		)
		if opts.ReThrowSerializationExceptions {
			return fmt.Errorf("cache: L2 deserialization (key %q): envelope: %w", key, err)
		}
		return nil
	}
	now := c.clock.Now()
	ttl := time.UnixMilli(env.PE).Sub(now)
	expiredLE := now.UnixMilli() - 1 // strictly before now: IsLogicallyExpired uses now.After(le)
	if ttl <= 0 || env.LE <= expiredLE {
		return nil // already gone or already expired
	}
	env.LE = expiredLE
	data, err = json.Marshal(env)
	if err != nil {
		return fmt.Errorf("cache: L2 serialization (key %q): envelope: %w", key, err)
	}
	if err := c.callL2(ctx, opts, func(ctx context.Context) error {
		return c.l2.Set(ctx, pk, data, ttl, env.Tags)
	}); err != nil {
		return c.l2Failed(ctx, opts, l2Op{name: opExpire, key: key}, err)
	}
	return nil
}
