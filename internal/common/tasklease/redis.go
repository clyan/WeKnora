package tasklease

import (
	"context"
	"errors"
	"time"

	"github.com/Tencent/WeKnora/internal/common/redislock"
	"github.com/redis/go-redis/v9"
)

var claimSlots = redis.NewScript(`
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', ARGV[1])
if redis.call('ZCARD', KEYS[1]) >= tonumber(ARGV[2]) then return 0 end
redis.call('ZADD', KEYS[1], ARGV[1] + ARGV[4], ARGV[3])
redis.call('PEXPIRE', KEYS[1], ARGV[4] * 2)
return 1
`)

var renewSlot = redis.NewScript(`
local expiry = redis.call('ZSCORE', KEYS[1], ARGV[1])
if not expiry or tonumber(expiry) <= tonumber(ARGV[2]) then return 0 end
redis.call('ZADD', KEYS[1], ARGV[2] + ARGV[3], ARGV[1])
redis.call('PEXPIRE', KEYS[1], ARGV[3] * 2)
return 1
`)

// TrySlots atomically claims one expiring slot in the caller's namespace.
// It does not choose a Wiki/Code Wiki/model limit or silently bypass errors.
func TrySlots(ctx context.Context, client redis.UniversalClient, key string, limit int, ttl time.Duration) (Lease, error) {
	if client == nil || key == "" || limit < 1 || ttl < time.Millisecond {
		return nil, errors.New("invalid background slot configuration")
	}
	token, err := redislock.NewToken()
	if err != nil {
		return nil, err
	}
	claimed, err := claimSlots.Run(ctx, client, []string{key}, time.Now().UnixMilli(), limit, token, ttl.Milliseconds()).Int()
	if err != nil {
		return nil, err
	}
	if claimed != 1 {
		return nil, ErrBusy
	}
	return Funcs{
		RenewFunc: func(ctx context.Context) error {
			owned, err := renewSlot.Run(ctx, client, []string{key}, token, time.Now().UnixMilli(), ttl.Milliseconds()).Int()
			if err != nil {
				return err
			}
			if owned != 1 {
				return ErrLost
			}
			return nil
		},
		ReleaseFunc: func(ctx context.Context) error { return client.ZRem(ctx, key, token).Err() },
	}, nil
}

// TryObject reuses the platform's compare-token Redis lock primitives.
func TryObject(ctx context.Context, client redis.UniversalClient, key string, ttl time.Duration) (Lease, error) {
	token, err := redislock.NewToken()
	if err != nil {
		return nil, err
	}
	owned, err := redislock.TryAcquire(ctx, client, key, token, ttl)
	if err != nil {
		return nil, err
	}
	if !owned {
		return nil, ErrBusy
	}
	return Funcs{
		RenewFunc: func(ctx context.Context) error {
			owned, err := redislock.Renew(ctx, client, key, token, ttl)
			if err != nil {
				return err
			}
			if !owned {
				return ErrLost
			}
			return nil
		},
		ReleaseFunc: func(ctx context.Context) error {
			owned, err := redislock.Release(ctx, client, key, token)
			if err != nil {
				return err
			}
			if !owned {
				return ErrLost
			}
			return nil
		},
	}, nil
}
