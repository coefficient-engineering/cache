package redis

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	defaultMaintenanceBatch = 128
	maximumMaintenanceBatch = 4096
)

// WithMaintenanceInterval sets the delay between complete passes. The first
// pass starts immediately when [Adapter.RunMaintenance] is called.
func WithMaintenanceInterval(interval time.Duration) Option {
	return func(a *Adapter) { a.maintenanceInterval = interval }
}

// WithMaintenanceBatchSize sets the SCAN work hint and metadata retirement
// batch size. Redis may return more records than its SCAN COUNT hint.
func WithMaintenanceBatchSize(size int64) Option {
	return func(a *Adapter) { a.maintenanceBatch = size }
}

// RunMaintenance repeatedly reclaims invalid records and unused-looking tag
// metadata. The caller owns its goroutine and context. Transient pass failures
// are logged and retried on the next interval; cancellation returns ctx.Err.
// Progress requires continued successful coverage of every primary.
func (a *Adapter) RunMaintenance(ctx context.Context) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := a.collect(ctx); err != nil && ctx.Err() == nil {
			a.logger.WarnContext(ctx, "redis l2: maintenance pass failed", "error", err)
		}
		timer := time.NewTimer(a.maintenanceInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

type collection struct {
	incarnation string
	candidates  map[string]string
	mu          sync.Mutex
}

func (a *Adapter) collect(ctx context.Context) error {
	snapshot, err := a.controlCall(ctx, []any{"prepare"})
	if err != nil {
		return err
	}
	header, err := snapshotHeader(snapshot, nil)
	if err != nil {
		return err
	}
	candidates, err := a.tagCandidates(ctx)
	if err != nil {
		return err
	}
	pass := &collection{incarnation: header.Incarnation, candidates: candidates}
	err = a.access.ForEachPrimary(ctx, func(ctx context.Context, primary *redis.Client) error {
		return a.collectPrimary(ctx, primary, pass)
	})
	if err != nil {
		return err
	}
	return a.retireTags(ctx, pass)
}

// scanReply decodes the public Redis SCAN-family wire reply without assuming
// that a custom transport returned correctly typed data.
func scanReply(reply []any) (uint64, []any, error) {
	if len(reply) != 2 {
		return 0, nil, errors.New("redis l2: invalid scan reply")
	}
	cursor, ok := reply[0].(string)
	if !ok {
		return 0, nil, errors.New("redis l2: invalid scan cursor")
	}
	next, err := strconv.ParseUint(cursor, 10, 64)
	if err != nil {
		return 0, nil, fmt.Errorf("redis l2: invalid scan cursor: %w", err)
	}
	values, ok := reply[1].([]any)
	if !ok || len(values)%2 != 0 {
		return 0, nil, errors.New("redis l2: invalid hash scan fields")
	}
	return next, values, nil
}

func (a *Adapter) tagCandidates(ctx context.Context) (map[string]string, error) {
	candidates := make(map[string]string)
	var cursor uint64
	for {
		reply, err := a.controlClient.Eval(ctx,
			"return redis.call('HSCAN', KEYS[1], ARGV[1], 'MATCH', 't:*', 'COUNT', ARGV[2])",
			[]string{a.controlKey()}, cursor, a.maintenanceBatch).Slice()
		if err != nil {
			return nil, err
		}
		next, fields, err := scanReply(reply)
		if err != nil {
			return nil, err
		}
		for i := 0; i < len(fields); i += 2 {
			field, fieldOK := fields[i].(string)
			generation, generationOK := fields[i+1].(string)
			if !fieldOK || !generationOK {
				return nil, errors.New("redis l2: invalid tag metadata")
			}
			candidates[field] = generation
		}
		cursor = next
		if cursor == 0 {
			return candidates, nil
		}
	}
}

func (a *Adapter) collectPrimary(ctx context.Context, primary *redis.Client, pass *collection) error {
	var cursor uint64
	pattern := escapeGlob(a.namespace()+"data:") + "*"
	for {
		keys, next, err := primary.Scan(ctx, cursor, pattern, a.maintenanceBatch).Result()
		if err != nil {
			return err
		}
		for _, key := range keys {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := a.collectRecord(ctx, primary, key, pass); err != nil {
				return err
			}
		}
		cursor = next
		if cursor == 0 {
			return nil
		}
	}
}

const conditionalDeleteScript = `
if redis.call('GET', KEYS[1]) == ARGV[1] then
 return redis.call('UNLINK', KEYS[1])
end
return 0
`

func (a *Adapter) collectRecord(ctx context.Context, primary *redis.Client, key string, pass *collection) error {
	// A Ring may have remapped this key since it was written. Reclaim the
	// physical version found on this shard, not a record on its new route.
	data, err := primary.Eval(ctx, "return redis.call('GET', KEYS[1])", []string{key}).Text()
	if errors.Is(err, redis.Nil) {
		return nil
	}
	if err != nil {
		return err
	}
	header, _, err := decodeRecord([]byte(data))
	if err != nil {
		return err
	}
	valid, err := a.validRecord(ctx, header)
	if err != nil {
		return err
	}
	if !valid {
		return primary.Eval(ctx, conditionalDeleteScript, []string{key}, data).Err()
	}
	pass.mu.Lock()
	for _, tag := range header.Tags {
		field := "t:" + tag.Tag
		if pass.candidates[field] == tag.Generation {
			delete(pass.candidates, field)
		}
	}
	pass.mu.Unlock()
	return nil
}

const retireTagsScript = `
if redis.call('HGET', KEYS[1], 'i') ~= ARGV[1] then return 0 end
for n = 2, #ARGV, 2 do
 if redis.call('HGET', KEYS[1], ARGV[n]) == ARGV[n+1] then
  redis.call('HDEL', KEYS[1], ARGV[n])
 end -- matching generation
end
return 1
`

func (a *Adapter) retireTags(ctx context.Context, pass *collection) error {
	args := make([]any, 1, 1+2*int(a.maintenanceBatch))
	args[0] = pass.incarnation
	for field, generation := range pass.candidates {
		args = append(args, field, generation)
		if len(args) == cap(args) {
			if err := a.controlClient.Eval(ctx, retireTagsScript, []string{a.controlKey()}, args...).Err(); err != nil {
				return err
			}
			args = args[:1]
		}
	}
	if len(args) == 1 {
		return nil
	}
	return a.controlClient.Eval(ctx, retireTagsScript, []string{a.controlKey()}, args...).Err()
}

func escapeGlob(value string) string {
	return strings.NewReplacer("\\", "\\\\", "*", "\\*", "?", "\\?", "[", "\\[", "]", "\\]").Replace(value)
}
