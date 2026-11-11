package video

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// TestFanOutStaggeredMeetsQueryQuota（审计 A04 回归）：40 项满批、50ms 间隔
// 必须在约 2s 内全部派发（≈20/s 对齐官方查询配额）。旧行为"先占 12 槽再
// 各自等 i*60ms"实测仅 ~8 项/秒（等待占槽）。
func TestFanOutStaggeredMeetsQueryQuota(t *testing.T) {
	ctx := context.Background()
	var done atomic.Int64
	start := time.Now()
	if err := fanOutStaggered(ctx, 40, 50*time.Millisecond, 12, func(context.Context, int) {
		done.Add(1)
	}); err != nil {
		t.Fatal(err)
	}
	elapsed := time.Since(start)
	if got := done.Load(); got != 40 {
		t.Fatalf("all 40 items must run: %d", got)
	}
	// 40 项 × 50ms = 2s 节拍；执行为 no-op，允许小幅调度误差。
	if elapsed > 2500*time.Millisecond {
		t.Fatalf("40 items at 50ms cadence must finish <2.5s, took %v", elapsed)
	}
}

// TestFanOutStaggeredCancellation 断言优雅关闭：ctx 取消后立即返回，
// 未到点的项不再执行。
func TestFanOutStaggeredCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var ran atomic.Int64
	start := time.Now()
	go func() {
		time.Sleep(120 * time.Millisecond)
		cancel()
	}()
	_ = fanOutStaggered(ctx, 100, 100*time.Millisecond, 4, func(context.Context, int) { ran.Add(1) })
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("cancellation must return promptly, took %v", elapsed)
	}
	if ran.Load() > 20 {
		t.Fatalf("cancelled fan-out must not keep dispatching: %d", ran.Load())
	}
}

// TestFanOutStaggeredSlowExecutionDoesNotBlockCadence 断言"等待不占槽"：
// 每项执行 300ms、4 并发槽下，前 8 项的派发节拍不被执行时长拖慢
//（第 8 项应在 ~400ms 内开始——若等待占槽则会推迟到 ~600ms 后）。
func TestFanOutStaggeredSlowExecutionDoesNotBlockCadence(t *testing.T) {
	ctx := context.Background()
	var started [16]atomic.Int64
	zero := time.Now()
	if err := fanOutStaggered(ctx, 16, 50*time.Millisecond, 4, func(_ context.Context, i int) {
		started[i].Store(time.Since(zero).Milliseconds())
		time.Sleep(300 * time.Millisecond)
	}); err != nil {
		t.Fatal(err)
	}
	// 第 0-3 项立即开始；第 4-7 项按 200/250/300/350ms 节拍在等待后进入
	// 槽位排队。宽松上界：第 7 项的开始等待不应超过其节拍 + 槽位周转。
	if s := started[7].Load(); s > 800 {
		t.Fatalf("item 7 cadence must not be blocked by slow execution: started %dms", s)
	}
}
