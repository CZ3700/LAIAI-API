package relay

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"
)

type fakeCounter struct {
	mu  sync.Mutex
	m   map[string]int64
	err error
}

func (f *fakeCounter) incr(_ context.Context, key string, _ time.Duration) (int64, error) {
	if f.err != nil {
		return 0, f.err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.m[key]++
	return f.m[key], nil
}

func rateTestRelay(rc rateCounter, rpm int) *Relay {
	return &Relay{
		Now:         time.Now,
		Log:         slog.New(slog.NewTextHandler(io.Discard, nil)),
		rate:        rc,
		rateEnabled: true, // 直接置缓存态，modelRatePolicy 走 5s 缓存不打库
		rateRPM:     rpm,
		ratePolAt:   time.Now(),
	}
}

// P2-2：Redis 路径下，同窗口内放行数恰好等于 RPM 上限。
func TestAllowModelRateRedisCountsToLimit(t *testing.T) {
	fc := &fakeCounter{m: map[string]int64{}}
	r := rateTestRelay(fc, 3)
	allowed := 0
	for i := 0; i < 6; i++ {
		if ok, _ := r.allowModelRate(1, "gpt-4o"); ok {
			allowed++
		}
	}
	if allowed != 3 {
		t.Fatalf("allowed = %d, want 3 (RPM limit via redis counter)", allowed)
	}
}

// P2-2 用户体验红线：Redis 出错一律 fail-open（绝不挡付费流量）。
func TestAllowModelRateFailsOpenOnRedisError(t *testing.T) {
	fc := &fakeCounter{m: map[string]int64{}, err: errors.New("redis unreachable")}
	r := rateTestRelay(fc, 1) // 极严上限，但 Redis 报错 → 全放行
	for i := 0; i < 20; i++ {
		if ok, _ := r.allowModelRate(1, "gpt-4o"); !ok {
			t.Fatalf("request %d blocked; must fail open on redis error", i)
		}
	}
}

// 不同 (org, model) 互不影响（key 隔离）。
func TestAllowModelRateKeyIsolation(t *testing.T) {
	fc := &fakeCounter{m: map[string]int64{}}
	r := rateTestRelay(fc, 1)
	if ok, _ := r.allowModelRate(1, "a"); !ok {
		t.Fatal("org1/a first call should pass")
	}
	if ok, _ := r.allowModelRate(2, "a"); !ok {
		t.Fatal("org2/a must be independent of org1")
	}
	if ok, _ := r.allowModelRate(1, "b"); !ok {
		t.Fatal("org1/b must be independent of org1/a")
	}
	if ok, _ := r.allowModelRate(1, "a"); ok {
		t.Fatal("org1/a second call should be blocked (limit 1)")
	}
}
