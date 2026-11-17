// Package redis implements a Cluster-safe Redis L2 cache. Payloads remain
// sharded; logical invalidation uses one authoritative generation hash.
// Applications run maintenance explicitly to reclaim invalidated records.
package redis

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/coefficient-engineering/cache/l2"
)

const (
	storageVersion = 2
)

// Option configures an Adapter.
type Option func(*Adapter)

// WithKeyPrefix isolates the adapter's versioned data and metadata namespace.
func WithKeyPrefix(prefix string) Option {
	return func(a *Adapter) { a.keyPrefix = prefix }
}

// WithLogger supplies the logger for recovery and maintenance diagnostics.
func WithLogger(logger *slog.Logger) Option {
	return func(a *Adapter) { a.logger = logger }
}

// Adapter implements [l2.Adapter] and [l2.TagsDeleter]. It does not own the
// injected clients. Metadata validation always uses primary routing.
type Adapter struct {
	client              redis.Cmdable
	controlClient       redis.Cmdable
	access              PrimaryAccess
	controlAccess       PrimaryAccess
	keyPrefix           string
	logger              *slog.Logger
	maintenanceInterval time.Duration
	maintenanceBatch    int64
}

// New validates client capabilities without performing Redis I/O.
// Native standalone and Cluster clients work directly. Ring requires
// [WithControlClient]; custom wrappers require [WithPrimaryAccess].
// The new versioned layout intentionally does not read legacy records.
func New(client redis.Cmdable, opts ...Option) (*Adapter, error) {
	a := &Adapter{
		client: client, logger: slog.New(slog.DiscardHandler),
		maintenanceInterval: time.Minute,
		maintenanceBatch:    defaultMaintenanceBatch,
	}
	for _, opt := range opts {
		opt(a)
	}
	if a.logger == nil {
		return nil, errors.New("redis l2: logger must not be nil")
	}
	if a.maintenanceInterval <= 0 || a.maintenanceBatch <= 0 || a.maintenanceBatch > maximumMaintenanceBatch {
		return nil, errors.New("redis l2: maintenance interval must be positive and batch size must be in [1, 4096]")
	}
	if err := a.configureAccess(); err != nil {
		return nil, err
	}
	return a, nil
}

func (a *Adapter) namespace() string  { return a.keyPrefix + "fc:v2:" }
func (a *Adapter) controlKey() string { return a.namespace() + "control" }
func (a *Adapter) pk(key string) string {
	return a.namespace() + "data:" + base64.RawURLEncoding.EncodeToString([]byte(key))
}

type tagGeneration struct {
	Tag        string `json:"t"`
	Generation string `json:"g"`
}

type recordHeader struct {
	Version     int             `json:"v"`
	Incarnation string          `json:"i"`
	Clear       string          `json:"c"`
	Tags        []tagGeneration `json:"t,omitempty"`
}

// Get returns only a record whose incarnation and every generation still
// match authoritative metadata. Missing metadata is never a default match.
func (a *Adapter) Get(ctx context.Context, key string) ([]byte, error) {
	data, err := a.client.Eval(ctx, "return redis.call('GET', KEYS[1])", []string{a.pk(key)}).Text()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	header, payload, err := decodeRecord([]byte(data))
	if err != nil {
		return nil, err
	}
	valid, err := a.validRecord(ctx, header)
	if err != nil || !valid {
		return nil, err
	}
	return payload, nil
}

func (a *Adapter) validRecord(ctx context.Context, header recordHeader) (bool, error) {
	const validationArguments = 3
	args := make([]any, validationArguments, validationArguments+2*len(header.Tags))
	args[0], args[1], args[2] = "validate", header.Incarnation, header.Clear
	for _, tag := range header.Tags {
		args = append(args, tag.Tag, tag.Generation)
	}
	value, err := a.controlCall(ctx, args)
	if err != nil {
		return false, err
	}
	valid, ok := value.(int64)
	if !ok {
		return false, errors.New("redis l2: invalid metadata validation reply")
	}
	return valid == 1, nil
}

// Set snapshots generations once, then stores a wrapper with native Redis
// expiry. Its data-write retries reuse identical bytes; TTL zero is infinite.
func (a *Adapter) Set(ctx context.Context, key string, value []byte, ttl time.Duration, tags []string) error {
	tags = slices.Clone(tags)
	for i, tag := range tags {
		tags[i] = base64.RawURLEncoding.EncodeToString([]byte(tag))
	}
	slices.Sort(tags)
	tags = slices.Compact(tags)
	args := make([]any, 1, len(tags)+1)
	args[0] = "prepare"
	for _, tag := range tags {
		args = append(args, tag)
	}
	result, err := a.controlCall(ctx, args)
	if err != nil {
		return err
	}
	header, err := snapshotHeader(result, tags)
	if err != nil {
		return err
	}
	metadata, err := json.Marshal(header)
	if err != nil {
		return err
	}
	// Compact JSON contains no literal newline. A single delimiter preserves
	// arbitrary payload bytes without base64 copies or size-narrowing casts.
	data := make([]byte, 0, len(metadata)+1+len(value))
	data = append(data, metadata...)
	data = append(data, '\n')
	data = append(data, value...)
	return a.client.Set(ctx, a.pk(key), data, ttl).Err()
}

func snapshotHeader(value any, tags []string) (recordHeader, error) {
	values, ok := value.([]any)
	if !ok || len(values) != len(tags)+2 {
		return recordHeader{}, errors.New("redis l2: invalid generation snapshot reply")
	}
	header := recordHeader{Version: storageVersion, Tags: make([]tagGeneration, len(tags))}
	var valid bool
	header.Incarnation, valid = values[0].(string)
	if !valid {
		return recordHeader{}, errors.New("redis l2: invalid incarnation reply")
	}
	header.Clear, valid = values[1].(string)
	if !valid {
		return recordHeader{}, errors.New("redis l2: invalid clear generation reply")
	}
	for i, tag := range tags {
		generation, ok := values[i+2].(string)
		if !ok {
			return recordHeader{}, errors.New("redis l2: invalid tag generation reply")
		}
		header.Tags[i] = tagGeneration{Tag: tag, Generation: generation}
	}
	return header, nil
}

func decodeRecord(data []byte) (recordHeader, []byte, error) {
	var header recordHeader
	metadata, payload, found := bytes.Cut(data, []byte{'\n'})
	if !found {
		return header, nil, errors.New("redis l2: truncated record header")
	}
	if err := json.Unmarshal(metadata, &header); err != nil {
		return header, nil, fmt.Errorf("redis l2: invalid record metadata: %w", err)
	}
	if header.Version != storageVersion || len(header.Incarnation) != 2*incarnationBytes || header.Clear == "" {
		return header, nil, errors.New("redis l2: invalid record version or generation")
	}
	for _, tag := range header.Tags {
		if tag.Generation == "" {
			return header, nil, errors.New("redis l2: empty tag generation")
		}
	}
	return header, payload, nil
}

// Delete removes one payload. Its former generation snapshots need no
// reverse-index bookkeeping; maintenance retires unused metadata.
func (a *Adapter) Delete(ctx context.Context, key string) error {
	return a.client.Del(ctx, a.pk(key)).Err()
}

// DeleteByTag logically invalidates a tag without enumerating payloads.
func (a *Adapter) DeleteByTag(ctx context.Context, tag string) error {
	return a.DeleteByTags(ctx, []string{tag})
}

// DeleteByTags mutates the shared metadata key. Failed operations may have
// partial effects; retries only advance generations, never restore them.
func (a *Adapter) DeleteByTags(ctx context.Context, tags []string) error {
	if len(tags) == 0 {
		return nil
	}
	args := make([]any, 1, len(tags)+1)
	args[0] = "invalidate"
	for _, tag := range tags {
		args = append(args, base64.RawURLEncoding.EncodeToString([]byte(tag)))
	}
	_, err := a.controlCall(ctx, args)
	return err
}

// Clear logically invalidates every record, including untagged records,
// in this adapter namespace. Maintenance reclaims the old bytes.
func (a *Adapter) Clear(ctx context.Context) error {
	_, err := a.controlCall(ctx, []any{"clear"})
	return err
}

// Ping checks that the payload and control backends are reachable.
func (a *Adapter) Ping(ctx context.Context) error {
	if err := a.client.Ping(ctx).Err(); err != nil {
		return err
	}
	return a.controlClient.Ping(ctx).Err()
}

var (
	_ l2.Adapter     = (*Adapter)(nil)
	_ l2.TagsDeleter = (*Adapter)(nil)
)
