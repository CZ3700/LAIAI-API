package relay

// 熔断器单测（breaker.go）：连败开闸、冷却半开单探活、探活成败转移、picker 降级保证。

import (
	"log/slog"
	"os"
	"testing"
	"time"
)

func newBreakerRelay() (*Relay, *time.Time) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	r := &Relay{
		Log: slog.New(slog.NewTextHandler(os.Stderr, nil)),
		Now: func() time.Time { return now },
	}
	return r, &now
}

func TestBreakerTripsAfterConsecutiveFailures(t *testing.T) {
	r, _ := newBreakerRelay()
	for i := 0; i < breakerThreshold-1; i++ {
		r.breakerNote(1, false)
		if !r.breakerAllow(1) {
			t.Fatalf("tripped early at fail %d (threshold=%d)", i+1, breakerThreshold)
		}
	}
	r.breakerNote(1, false) // 第 threshold 次
	if r.breakerAllow(1) {
		t.Fatal("want open after threshold consecutive failures")
	}
	// 中途成功清零：另一渠道不受影响
	if !r.breakerAllow(2) {
		t.Fatal("channel 2 must be unaffected")
	}
}

func TestBreakerSuccessResetsCount(t *testing.T) {
	r, _ := newBreakerRelay()
	for i := 0; i < breakerThreshold-1; i++ {
		r.breakerNote(1, false)
	}
	r.breakerNote(1, true) // 成功清零
	for i := 0; i < breakerThreshold-1; i++ {
		r.breakerNote(1, false)
	}
	if !r.breakerAllow(1) {
		t.Fatal("count must reset on success (4+4 non-consecutive fails must not trip)")
	}
}

func TestBreakerHalfOpenSingleProbe(t *testing.T) {
	r, nowPtr := newBreakerRelay()
	for i := 0; i < breakerThreshold; i++ {
		r.breakerNote(1, false)
	}
	if r.breakerAllow(1) {
		t.Fatal("want open")
	}
	*nowPtr = nowPtr.Add(breakerCooldown + time.Second) // 冷却期满
	if !r.breakerAllow(1) {
		t.Fatal("half-open must admit exactly one probe")
	}
	if r.breakerAllow(1) {
		t.Fatal("second concurrent probe must be rejected")
	}
	// 探活成功 → 闭合恢复全量（连续两次询问都放行，不再是单探活名额）
	r.breakerNote(1, true)
	first := r.breakerAllow(1)
	second := r.breakerAllow(1)
	if !first || !second {
		t.Fatal("closed after successful probe: all traffic admitted")
	}
}

func TestBreakerProbeFailureReopens(t *testing.T) {
	r, nowPtr := newBreakerRelay()
	for i := 0; i < breakerThreshold; i++ {
		r.breakerNote(1, false)
	}
	*nowPtr = nowPtr.Add(breakerCooldown + time.Second)
	if !r.breakerAllow(1) {
		t.Fatal("probe admit")
	}
	r.breakerNote(1, false) // 探活失败 → 重回冷却
	if r.breakerAllow(1) {
		t.Fatal("want re-open after failed probe")
	}
	*nowPtr = nowPtr.Add(breakerCooldown + time.Second)
	if !r.breakerAllow(1) {
		t.Fatal("second probe after second cooldown")
	}
}

func TestBreakerProbeExpiryReclaims(t *testing.T) {
	r, nowPtr := newBreakerRelay()
	for i := 0; i < breakerThreshold; i++ {
		r.breakerNote(1, false)
	}
	*nowPtr = nowPtr.Add(breakerCooldown + time.Second)
	if !r.breakerAllow(1) {
		t.Fatal("probe admit")
	}
	// 探活请求丢失（永无 note）：超时后名额可再认领，渠道不会卡死在半开
	*nowPtr = nowPtr.Add(breakerProbeExpiry + time.Second)
	if !r.breakerAllow(1) {
		t.Fatal("expired probe slot must be reclaimable")
	}
}

// picker 降级保证：全部候选被熔断挡住 → reopen 清除排除并 bypass，可用性不比无熔断差。
func TestBreakerPickerReopenBypass(t *testing.T) {
	r, _ := newBreakerRelay()
	for i := 0; i < breakerThreshold; i++ {
		r.breakerNote(7, false)
	}
	p := r.newBreakerPicker()
	exclude := map[uint64]bool{}
	if p.admit(7, exclude) {
		t.Fatal("open channel must be rejected")
	}
	if !exclude[7] {
		t.Fatal("rejected channel must be excluded")
	}
	// 模拟 pick 失败 → reopen
	if !p.reopen(exclude) {
		t.Fatal("reopen must succeed when channels were breaker-skipped")
	}
	if exclude[7] {
		t.Fatal("reopen must clear breaker-caused excludes")
	}
	if !p.admit(7, exclude) {
		t.Fatal("bypass mode must admit everything")
	}
	// 没有熔断跳过时 reopen 返回 false（真无渠道）
	p2 := r.newBreakerPicker()
	if p2.reopen(map[uint64]bool{}) {
		t.Fatal("reopen without breaker skips must return false")
	}
}
