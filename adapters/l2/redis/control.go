package redis

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/redis/go-redis/v9"
)

const (
	controlAttempts  = 3
	incarnationBytes = 16
)

// Every script accesses only its declared key. Missing control state is a
// distinct reply, never an instruction to reset the retained sequence.
const controlScript = `
local incarnation = redis.call('HGET', KEYS[1], 'i')
if not incarnation then return false end
local clear = redis.call('HGET', KEYS[1], 'c')
local sequence = redis.call('HGET', KEYS[1], 's')
if not clear or not sequence then
 return redis.error_reply('redis l2: corrupt control anchor')
end
local function allocate()
 if sequence == '9223372036854775807' then error('redis l2: generation sequence exhausted') end
 redis.call('HINCRBY', KEYS[1], 's', 1)
 sequence = redis.call('HGET', KEYS[1], 's')
 return sequence
end
local op = ARGV[1]
if op == 'validate' then
 if incarnation ~= ARGV[2] or clear ~= ARGV[3] then return 0 end
 for n = 4, #ARGV, 2 do
  if redis.call('HGET', KEYS[1], 't:' .. ARGV[n]) ~= ARGV[n+1] then return 0 end -- mismatch
 end
 return 1
end
if op == 'clear' then
 redis.call('HSET', KEYS[1], 'c', allocate())
 return 1
end
local result = {incarnation, clear}
for n = 2, #ARGV do
 local field = 't:' .. ARGV[n]
 local generation = redis.call('HGET', KEYS[1], field)
 if op == 'invalidate' or not generation then
  generation = allocate()
  redis.call('HSET', KEYS[1], field, generation)
 end
 result[#result+1] = generation
end
return result
`

const initializeScript = `
if redis.call('EXISTS', KEYS[1]) == 1 then
 if not redis.call('HGET', KEYS[1], 'i') or not redis.call('HGET', KEYS[1], 's') or not redis.call('HGET', KEYS[1], 'c') then
  return redis.error_reply('redis l2: corrupt control anchor')
 end
 return 1
end
redis.call('HSET', KEYS[1], 'i', ARGV[1], 's', '0', 'c', '0')
return 1
`

func (a *Adapter) controlCall(ctx context.Context, args []any) (any, error) {
	for range controlAttempts {
		value, err := a.controlClient.Eval(ctx, controlScript, []string{a.controlKey()}, args...).Result()
		if !errors.Is(err, redis.Nil) {
			return value, err
		}
		if err := a.recoverControl(ctx); err != nil {
			return nil, err
		}
	}
	return nil, errors.New("redis l2: control state repeatedly disappeared")
}

// recoverControl never submits an incarnation candidate twice. Even a lost
// reply requires a fresh candidate: reusing one could revive older records.
func (a *Adapter) recoverControl(ctx context.Context) error {
	primary, err := a.controlAccess.PrimaryForKey(ctx, a.controlKey())
	if err != nil {
		return err
	}
	var route initializationRoute
	for range controlAttempts {
		candidate := make([]byte, incarnationBytes)
		if _, err := rand.Read(candidate); err != nil {
			return fmt.Errorf("redis l2: incarnation randomness: %w", err)
		}
		err = initializeOnce(ctx, primary, route, a.controlKey(), hex.EncodeToString(candidate))
		if err == nil {
			a.logger.WarnContext(ctx, "redis l2: control state initialized; older incarnations are invalid", "namespace", a.keyPrefix)
			return nil
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		route = controlRedirect(err)
	}
	return fmt.Errorf("redis l2: automatic control recovery: %w", err)
}

type initializationRoute struct {
	address string
	asking  bool
}

// initializeOnce uses a private, non-retrying connection pool. Neither
// ClusterClient's outer retry loop nor caller hooks can replay the command.
func initializeOnce(ctx context.Context, primary *redis.Client, route initializationRoute, key, candidate string) error {
	options := *primary.Options()
	options.MaxRetries = -1
	options.PoolSize = 1
	options.MinIdleConns = 0
	if route.address != "" {
		options.Addr = route.address
	}
	// NewClient may normalize this configuration; never share that mutable
	// configuration object with the caller's connection pool.
	if options.MaintNotificationsConfig != nil {
		config := *options.MaintNotificationsConfig
		options.MaintNotificationsConfig = &config
	}
	client := redis.NewClient(&options)
	defer client.Close()
	if !route.asking {
		return client.Eval(ctx, initializeScript, []string{key}, candidate).Err()
	}
	_, err := client.Pipelined(ctx, func(pipe redis.Pipeliner) error {
		pipe.Do(ctx, "ASKING")
		pipe.Eval(ctx, initializeScript, []string{key}, candidate)
		return nil
	})
	return err
}

func controlRedirect(err error) initializationRoute {
	const redirectFields = 3
	fields := strings.Fields(err.Error())
	if len(fields) != redirectFields {
		return initializationRoute{}
	}
	switch fields[0] {
	case "MOVED":
		return initializationRoute{address: fields[2]}
	case "ASK":
		return initializationRoute{address: fields[2], asking: true}
	default:
		return initializationRoute{}
	}
}
