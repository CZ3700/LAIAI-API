package alert

import (
	"context"
	"log/slog"
	"sync"
	"testing"
	"time"
)

func TestIsCritical(t *testing.T) {
	crit := []string{
		"SETTLE FAILED — manual reconcile needed",
		"LEDGER DRIFT DETECTED — manual reconcile needed",
		"ledger drift check failed",
		"AUDIO SETTLE FAILED — manual reconcile needed",
		"x402 SETTLED ON-CHAIN BUT BOTH CREDIT AND PROOF-PERSIST FAILED — manual credit needed",
		"provider_ref persist failed — reconcile blind spot if callback misses",
		"topup reconcile failed",
	}
	for _, m := range crit {
		if !isCritical(m) {
			t.Errorf("expected critical: %q", m)
		}
	}

	notCrit := []string{
		"config load failed",       // 启动错误，非资金事件
		"db connect failed",        // 连接错误
		"server failed",            // 服务器错误
		"log retention purge failed", // 清理失败，非资金
		"released stuck relay holds", // 正常 sweep（且是 Warn 不是 Error）
	}
	for _, m := range notCrit {
		if isCritical(m) {
			t.Errorf("expected NOT critical: %q", m)
		}
	}
}

func TestDeduperWindow(t *testing.T) {
	base := time.Unix(1_700_000_000, 0)
	cur := base
	d := newDeduper(5 * time.Minute)
	d.now = func() time.Time { return cur }

	if !d.allow("SETTLE FAILED") {
		t.Fatal("first call should be allowed")
	}
	if d.allow("SETTLE FAILED") {
		t.Fatal("second call within window should be suppressed")
	}
	// 不同 key 不受影响
	if !d.allow("DRIFT DETECTED") {
		t.Fatal("different key should be allowed")
	}
	// 窗口过后同 key 再次放行
	cur = base.Add(6 * time.Minute)
	if !d.allow("SETTLE FAILED") {
		t.Fatal("after window, same key should be allowed again")
	}
}

// captureHandler 记录 inner 是否总被调用（告警不能吞掉正常日志）。
type captureHandler struct {
	mu   sync.Mutex
	msgs []string
}

func (c *captureHandler) Enabled(context.Context, slog.Level) bool { return true }
func (c *captureHandler) Handle(_ context.Context, r slog.Record) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.msgs = append(c.msgs, r.Message)
	return nil
}
func (c *captureHandler) WithAttrs([]slog.Attr) slog.Handler { return c }
func (c *captureHandler) WithGroup(string) slog.Handler      { return c }

// 未配 webhook 时：Handler 必须是纯 pass-through，绝不 panic，inner 照常收到全部日志。
func TestPassThroughWhenNoWebhook(t *testing.T) {
	cap := &captureHandler{}
	h := NewHandler(cap) // LAIAI_ALERT_WEBHOOK 未设 → webhook==""
	if h.webhook != "" {
		t.Skip("LAIAI_ALERT_WEBHOOK is set in this env; skip pass-through assertion")
	}
	log := slog.New(h)

	log.Info("hello")
	log.Error("SETTLE FAILED — manual reconcile needed", "hold", 42) // 即便 critical，无 webhook 也只走 inner

	cap.mu.Lock()
	defer cap.mu.Unlock()
	if len(cap.msgs) != 2 {
		t.Fatalf("inner should receive all 2 records, got %d: %v", len(cap.msgs), cap.msgs)
	}
}

// WithAttrs 派生后仍保留告警配置（root logger 会 .With("service",...)）。
func TestWithAttrsPreservesConfig(t *testing.T) {
	cap := &captureHandler{}
	h := NewHandler(cap)
	derived := h.WithAttrs([]slog.Attr{slog.String("service", "laiai-core")})
	if _, ok := derived.(*Handler); !ok {
		t.Fatal("WithAttrs must return *Handler to keep alert behavior on derived loggers")
	}
}
