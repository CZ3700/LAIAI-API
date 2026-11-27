package relay

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// rateCounter：跨实例固定窗口计数器抽象（便于测试注入）。incr 在 key 上原子自增并设过期，
// 返回自增后的计数。
type rateCounter interface {
	incr(ctx context.Context, key string, ttl time.Duration) (int64, error)
}

// tpmCounter provides atomic TPM reserve and post-response reconciliation.
type tpmCounter interface {
	reserve(ctx context.Context, key string, limit, tokens int64, ttl time.Duration) (bool, error)
	reconcile(ctx context.Context, key string, delta int64, ttl time.Duration) error
}

// redisRateCounter：go-redis 实现。INCR + EXPIRE（key 含窗口纪元，过期仅为清理）。
type redisRateCounter struct{ cli *redis.Client }

func (c redisRateCounter) incr(ctx context.Context, key string, ttl time.Duration) (int64, error) {
	pipe := c.cli.Pipeline()
	n := pipe.Incr(ctx, key)
	pipe.Expire(ctx, key, ttl)
	if _, err := pipe.Exec(ctx); err != nil {
		return 0, err
	}
	return n.Val(), nil
}

var (
	reserveTPMScript = redis.NewScript(`
local cur = tonumber(redis.call('GET', KEYS[1]) or '0')
local limit = tonumber(ARGV[1])
local add = tonumber(ARGV[2])
if cur + add > limit then
  return -1
end
local nextv = redis.call('INCRBY', KEYS[1], add)
redis.call('PEXPIRE', KEYS[1], tonumber(ARGV[3]))
return nextv
`)
	reconcileTPMScript = redis.NewScript(`
local delta = tonumber(ARGV[1])
if delta == 0 then
  return 0
end
local cur = tonumber(redis.call('GET', KEYS[1]) or '0')
local nextv = cur + delta
if nextv <= 0 then
  redis.call('SET', KEYS[1], '0', 'PX', tonumber(ARGV[2]))
  return 0
end
local nextStored = redis.call('INCRBY', KEYS[1], delta)
redis.call('PEXPIRE', KEYS[1], tonumber(ARGV[2]))
return nextStored
`)
)

type redisTPMCounter struct{ cli *redis.Client }

func (c redisTPMCounter) reserve(ctx context.Context, key string, limit, tokens int64, ttl time.Duration) (bool, error) {
	res, err := reserveTPMScript.Run(ctx, c.cli, []string{key}, limit, tokens, max(int64(1), ttl.Milliseconds())).Int64()
	if err != nil {
		return false, err
	}
	return res >= 0, nil
}

func (c redisTPMCounter) reconcile(ctx context.Context, key string, delta int64, ttl time.Duration) error {
	_, err := reconcileTPMScript.Run(ctx, c.cli, []string{key}, delta, max(int64(1), ttl.Milliseconds())).Result()
	return err
}

// UseRedisRateLimiter 接入 Redis 跨实例限流（cmd/core 在 LAIAI_REDIS_ADDR 配置时调用）。
func (r *Relay) UseRedisRateLimiter(cli *redis.Client) {
	if cli != nil {
		r.rate = redisRateCounter{cli: cli}
		r.tpm = redisTPMCounter{cli: cli}
		r.aff.useRedis(cli)
	}
}

type rateWindow struct {
	start time.Time
	count int
}

type tpmWindow struct {
	epoch  int64
	tokens int64
}

// modelRatePolicy：返回 (是否启用, 每分钟请求上限, 每分钟 Token 上限)。带 5s 缓存。
func (r *Relay) modelRatePolicy() (enabled bool, rpm int, tpm int) {
	now := r.Now()
	r.ratePolMu.Lock()
	if now.Sub(r.ratePolAt) < 5*time.Second {
		en, rLimit, tLimit := r.rateEnabled, r.rateRPM, r.rateTPM
		r.ratePolMu.Unlock()
		return en, rLimit, tLimit
	}
	r.ratePolMu.Unlock()

	var rows []struct{ Name, Value string }
	r.DB.Table("ext_settings").Where("name IN ?",
		[]string{"model_rate_limit_enabled", "model_rate_limit_rpm", "model_rate_limit_tpm"}).Find(&rows)
	for _, row := range rows {
		switch row.Name {
		case "model_rate_limit_enabled":
			enabled = row.Value == "true"
		case "model_rate_limit_rpm":
			if n, err := strconv.Atoi(row.Value); err == nil && n > 0 {
				rpm = n
			}
		case "model_rate_limit_tpm":
			if n, err := strconv.Atoi(row.Value); err == nil && n > 0 {
				tpm = n
			}
		}
	}
	r.ratePolMu.Lock()
	r.rateEnabled, r.rateRPM, r.rateTPM, r.ratePolAt = enabled, rpm, tpm, now
	r.ratePolMu.Unlock()
	return enabled, rpm, tpm
}

// allowModelRate：(org, model) 在当前 60s 窗口内是否还可放行。未启用/RPM=0 一律放行。
func (r *Relay) allowModelRate(orgID uint64, model string) (ok bool, rpm int) {
	enabled, limit, _ := r.modelRatePolicy()
	if !enabled || limit <= 0 {
		return true, 0
	}
	return r.allowWindowed(fmt.Sprintf("m:%d:%s", orgID, model), limit), limit
}

// allowKeyRate：key 级 RPM（0041，api_keys.rpm_limit）。0=不限。窗口/计数器与模型级共用。
func (r *Relay) allowKeyRate(keyID uint64, limit int) bool {
	if limit <= 0 {
		return true
	}
	return r.allowWindowed(fmt.Sprintf("k:%d", keyID), limit)
}

// allowKeyRPS：key 级每秒请求上限（api_keys.rps_limit）。0=不限。
func (r *Relay) allowKeyRPS(keyID uint64, limit int) bool {
	if limit <= 0 {
		return true
	}
	return r.allowWindowedDur(fmt.Sprintf("ks:%d", keyID), limit, time.Second)
}

// AllowModelCreateRPM：平台级每模型受理 RPM（task.Gate）。
func (r *Relay) AllowModelCreateRPM(model string, limit int) bool {
	if limit <= 0 {
		return true
	}
	return r.allowWindowed(fmt.Sprintf("vm:%s", model), limit)
}

func (r *Relay) allowWindowed(bucket string, limit int) bool {
	return r.allowWindowedDur(bucket, limit, time.Minute)
}

// allowWindowedDur：固定窗口计数（支持秒/分窗口）。Redis 跨实例，未配则进程内；
// fail-open（限流是保护、不是收费闸）。
func (r *Relay) allowWindowedDur(bucket string, limit int, windowDur time.Duration) bool {
	now := r.Now()
	secs := max(int64(1), int64(windowDur.Seconds()))

	if r.rate != nil {
		window := now.Unix() / secs
		key := fmt.Sprintf("rl:%s:%d", bucket, window)
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		n, err := r.rate.incr(ctx, key, windowDur*2)
		if err != nil {
			r.Log.Warn("rate limiter redis error, failing open", "bucket", bucket, "err", err)
			return true
		}
		return n <= int64(limit)
	}

	r.rateMu.Lock()
	defer r.rateMu.Unlock()
	if now.Sub(r.rateLastEvic) > 5*time.Minute {
		for k, w := range r.rateWindows {
			if now.Sub(w.start) > 2*time.Minute {
				delete(r.rateWindows, k)
			}
		}
		r.rateLastEvic = now
	}
	w := r.rateWindows[bucket]
	if w == nil || now.Sub(w.start) >= windowDur {
		r.rateWindows[bucket] = &rateWindow{start: now, count: 1}
		return true
	}
	if w.count >= limit {
		return false
	}
	w.count++
	return true
}

// reserveTPM reserves estimated input tokens across per-key and per-(org, model) 60s TPM windows.
// Returns the epoch used for later reconciliation and true if permitted.
func (r *Relay) reserveTPM(ctx context.Context, keyID uint64, keyTPMLimit int, orgID uint64, model string, estTokens int64) (epoch int64, ok bool) {
	if estTokens <= 0 {
		estTokens = 1
	}
	enabled, _, modelTPMLimit := r.modelRatePolicy()
	if !enabled {
		modelTPMLimit = 0
	}
	if keyTPMLimit <= 0 && modelTPMLimit <= 0 {
		return 0, true
	}
	epoch = r.Now().Unix() / 60
	keyBucket := fmt.Sprintf("tpm:k:%d:%d", keyID, epoch)
	modBucket := fmt.Sprintf("tpm:m:%d:%s:%d", orgID, model, epoch)

	if keyTPMLimit > 0 {
		if !r.reserveTPMBucket(ctx, keyBucket, int64(keyTPMLimit), estTokens) {
			return epoch, false
		}
	}
	if modelTPMLimit > 0 {
		if !r.reserveTPMBucket(ctx, modBucket, int64(modelTPMLimit), estTokens) {
			if keyTPMLimit > 0 {
				r.reconcileTPMBucket(ctx, keyBucket, -estTokens)
			}
			return epoch, false
		}
	}
	return epoch, true
}

func (r *Relay) reconcileTPM(ctx context.Context, epoch int64, keyID uint64, keyTPMLimit int, orgID uint64, model string, deltaTokens int64) {
	if epoch == 0 || deltaTokens == 0 {
		return
	}
	enabled, _, modelTPMLimit := r.modelRatePolicy()
	if !enabled {
		modelTPMLimit = 0
	}
	if keyTPMLimit > 0 {
		r.reconcileTPMBucket(ctx, fmt.Sprintf("tpm:k:%d:%d", keyID, epoch), deltaTokens)
	}
	if modelTPMLimit > 0 {
		r.reconcileTPMBucket(ctx, fmt.Sprintf("tpm:m:%d:%s:%d", orgID, model, epoch), deltaTokens)
	}
}

func (r *Relay) reserveTPMBucket(ctx context.Context, bucket string, limit, tokens int64) bool {
	if r.tpm != nil {
		rctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		allowed, err := r.tpm.reserve(rctx, "rl:"+bucket, limit, tokens, 2*time.Minute)
		if err != nil {
			r.Log.Warn("tpm limiter redis error, failing open", "bucket", bucket, "err", err)
			return true
		}
		return allowed
	}

	r.rateMu.Lock()
	defer r.rateMu.Unlock()
	if r.tpmWindows == nil {
		r.tpmWindows = make(map[string]*tpmWindow)
	}
	epoch := r.Now().Unix() / 60
	if len(r.tpmWindows) > 4096 {
		for k, w := range r.tpmWindows {
			if epoch-w.epoch > 2 {
				delete(r.tpmWindows, k)
			}
		}
	}
	w := r.tpmWindows[bucket]
	if w == nil || w.epoch != epoch {
		w = &tpmWindow{epoch: epoch}
		r.tpmWindows[bucket] = w
	}
	if w.tokens+tokens > limit {
		return false
	}
	w.tokens += tokens
	return true
}

func (r *Relay) reconcileTPMBucket(ctx context.Context, bucket string, delta int64) {
	if delta == 0 {
		return
	}
	if r.tpm != nil {
		rctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		defer cancel()
		if err := r.tpm.reconcile(rctx, "rl:"+bucket, delta, 2*time.Minute); err != nil {
			r.Log.Warn("tpm reconcile redis error", "bucket", bucket, "err", err)
		}
		return
	}

	r.rateMu.Lock()
	defer r.rateMu.Unlock()
	if r.tpmWindows == nil {
		return
	}
	w := r.tpmWindows[bucket]
	if w == nil {
		return
	}
	w.tokens = max(int64(0), w.tokens+delta)
}
