// Package redis is an implementation of the L2 cache adapter using Redis
// via the go-redis client library.
//
// It accepts redis.Cmdable, so it works with *redis.Client, *redis.ClusterClient,
// and *redis.Ring. Tag tracking uses Redis SETs alongside each data key.
// DeleteByTag uses a Lua script for atomic cleanup.
package redis

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"time"

	"github.com/coefficient-engineering/cache/l2"
	"github.com/redis/go-redis/v9"
)

// Option configures an Adapter.
type Option func(*Adapter)

// WithKeyPrefix sets a prefix prepended to every key stored in Redis.
// Use this to namespace keys when multiple services share a Redis instance.
func WithKeyPrefix(prefix string) Option {
	return func(a *Adapter) { a.keyPrefix = prefix }
}

// WithLogger sets a structured logger for diagnostic output.
func WithLogger(logger *slog.Logger) Option {
	return func(a *Adapter) { a.logger = logger }
}

// Adapter implements l2.Adapter backed by Redis.
type Adapter struct {
	client    redis.Cmdable
	keyPrefix string
	logger    *slog.Logger
}

// New creates a Redis L2 adapter.
//
// client can be *redis.Client, *redis.ClusterClient, or *redis.Ring.
// Options configure key prefix and logging.
func New(client redis.Cmdable, opts ...Option) *Adapter {
	a := &Adapter{
		client: client,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	for _, opt := range opts {
		opt(a)
	}
	return a
}

// pk returns the prefixed key for data storage.
func (a *Adapter) pk(key string) string { return a.keyPrefix + key }

// tagsKey returns the key that stores the set of tag names for a data key.
// Format: {prefix}tags:{key}
func (a *Adapter) tagsKey(key string) string { return a.keyPrefix + "tags:" + key }

// tagSetKey returns the key for the SET that tracks all data keys carrying a tag.
// Format: {prefix}tag:{tag}
func (a *Adapter) tagSetKey(tag string) string { return a.keyPrefix + "tag:" + tag }

func (a *Adapter) Get(ctx context.Context, key string) ([]byte, error) {
	data, err := a.client.Get(ctx, a.pk(key)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, nil // miss, NOT an error
	}
	if err != nil {
		return nil, err
	}
	return data, nil
}

func (a *Adapter) Set(ctx context.Context, key string, value []byte, ttl time.Duration, tags []string) error {
	pk := a.pk(key)

	// If overwriting, clean up old tag associations first.
	a.cleanOldTags(ctx, key)

	pipe := a.client.Pipeline()

	// Store the data.
	pipe.Set(ctx, pk, value, ttl)

	if len(tags) > 0 {
		// Store the list of tags associated with this key (for cleanup on Delete).
		tagNames := make([]any, len(tags))
		for i, t := range tags {
			tagNames[i] = t
		}
		pipe.Del(ctx, a.tagsKey(key))
		pipe.SAdd(ctx, a.tagsKey(key), tagNames...)
		if ttl > 0 {
			// Give the tags key a generous TTL so it outlives the data key,
			// but still gets cleaned up eventually.
			pipe.Expire(ctx, a.tagsKey(key), ttl*2)
		}

		// Add this key to each tag's member set.
		for _, tag := range tags {
			tsk := a.tagSetKey(tag)
			pipe.SAdd(ctx, tsk, pk)
			if ttl > 0 {
				pipe.Expire(ctx, tsk, ttl*2)
			}
		}
	}

	_, err := pipe.Exec(ctx)
	return err
}

// cleanOldTags removes the key from any tag sets it was previously associated with.
func (a *Adapter) cleanOldTags(ctx context.Context, key string) {
	pk := a.pk(key)
	oldTags, err := a.client.SMembers(ctx, a.tagsKey(key)).Result()
	if err != nil || len(oldTags) == 0 {
		return
	}
	pipe := a.client.Pipeline()
	for _, tag := range oldTags {
		pipe.SRem(ctx, a.tagSetKey(tag), pk)
	}
	pipe.Del(ctx, a.tagsKey(key))
	if _, err := pipe.Exec(ctx); err != nil {
		a.logger.Warn("redis l2: failed to clean old tags",
			slog.String("key", key), slog.String("error", err.Error()))
	}
}

func (a *Adapter) Delete(ctx context.Context, key string) error {
	pk := a.pk(key)

	// Read which tags this key belongs to, then clean up.
	oldTags, _ := a.client.SMembers(ctx, a.tagsKey(key)).Result()

	pipe := a.client.Pipeline()
	pipe.Del(ctx, pk)
	pipe.Del(ctx, a.tagsKey(key))
	for _, tag := range oldTags {
		pipe.SRem(ctx, a.tagSetKey(tag), pk)
	}
	_, err := pipe.Exec(ctx)
	return err
}

// deleteByTagScript is a Lua script that atomically:
// 1. Reads all data keys from the tag set
// 2. Deletes each data key and its tags:{key} metadata key
// 3. Deletes the tag set itself
//
// KEYS[1] = the tag set key (e.g., "prefix:tag:featured")
// ARGV[1] = the key prefix (e.g., "prefix:")
//
// Returns the number of data keys deleted.
var deleteByTagScript = redis.NewScript(`
local tag_set_key = KEYS[1]
local prefix = ARGV[1]

local members = redis.call('SMEMBERS', tag_set_key)
local deleted = 0

for _, data_key in ipairs(members) do
    local original_key = string.sub(data_key, #prefix + 1)
    local tags_key = prefix .. 'tags:' .. original_key

    -- Remove this key from ALL its associated tag sets (not just the one we're clearing)
    local all_tags = redis.call('SMEMBERS', tags_key)
    for _, t in ipairs(all_tags) do
        local other_tag_set = prefix .. 'tag:' .. t
        redis.call('SREM', other_tag_set, data_key)
    end

    redis.call('DEL', data_key)
    redis.call('DEL', tags_key)
    deleted = deleted + 1
end

redis.call('DEL', tag_set_key)
return deleted
`)

func (a *Adapter) DeleteByTag(ctx context.Context, tag string) error {
	_, err := deleteByTagScript.Run(ctx, a.client, []string{a.tagSetKey(tag)}, a.keyPrefix).Result()
	if errors.Is(err, redis.Nil) {
		return nil // tag set didn't exist
	}
	return err
}

func (a *Adapter) Clear(ctx context.Context) error {
	if a.keyPrefix == "" {
		// Without a prefix we cannot safely scope the clear.
		// Use FLUSHDB as a last resort, caller must be aware this
		// clears the entire database.
		return a.client.FlushDB(ctx).Err()
	}

	// SCAN-based deletion scoped to our key prefix.
	var cursor uint64
	pattern := a.keyPrefix + "*"
	for {
		keys, nextCursor, err := a.client.Scan(ctx, cursor, pattern, 100).Result()
		if err != nil {
			return err
		}
		if len(keys) > 0 {
			if err := a.client.Del(ctx, keys...).Err(); err != nil {
				return err
			}
		}
		cursor = nextCursor
		if cursor == 0 {
			break
		}
	}
	return nil
}

func (a *Adapter) Ping(ctx context.Context) error {
	return a.client.Ping(ctx).Err()
}

var _ l2.Adapter = (*Adapter)(nil)
