package leader

import (
	"context"
	"time"

	"github.com/redis/go-redis/v9"
)

// leaseScript：原子 acquire-or-renew。
//   - 我持有(value==token) → 续期 PEXPIRE，返回 1
//   - 空闲 → SET NX PX 抢占，返回 1
//   - 他人持有 → 返回 0
var leaseScript = redis.NewScript(`
if redis.call("get", KEYS[1]) == ARGV[1] then
  redis.call("pexpire", KEYS[1], ARGV[2])
  return 1
elseif redis.call("set", KEYS[1], ARGV[1], "NX", "PX", ARGV[2]) then
  return 1
else
  return 0
end
`)

type redisStore struct{ cli *redis.Client }

// NewRedisStore：基于 go-redis 的租约存储。cli=nil 返回 nil（调用方据此走单实例恒 leader）。
func NewRedisStore(cli *redis.Client) Store {
	if cli == nil {
		return nil
	}
	return redisStore{cli: cli}
}

func (s redisStore) acquireOrRenew(ctx context.Context, key, token string, ttl time.Duration) (bool, error) {
	res, err := leaseScript.Run(ctx, s.cli, []string{key}, token, ttl.Milliseconds()).Int()
	if err != nil {
		return false, err
	}
	return res == 1, nil
}
