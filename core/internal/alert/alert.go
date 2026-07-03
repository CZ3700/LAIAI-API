// Package alert 把 critical 级日志事件（资金漂移 / 结算失败 / 链上已扣但入账失败
// 等「需分钟级人工介入」的事件）通过飞书自定义机器人 webhook 主动推送告警。
//
// 设计为 slog.Handler 装饰器：包在 root logger 外层一次，即覆盖全部经它派生的
// 日志（main / server / relay），零改业务代码；未来新增的 critical log.Error 也
// 自动纳入。这样避免了在 8+ 处高危点逐个插桩、易漏的问题。
//
// 降级安全（绝不因告警拖垮业务）：
//   - 未配置 LAIAI_ALERT_WEBHOOK → 退化为纯 pass-through，行为等同未接告警；
//   - 推送在独立 goroutine + 3s 超时 + recover 中进行，永不阻塞或 panic 调用方；
//   - 同一消息在窗口内去重，防漂移巡检/突发结算失败刷屏。
package alert

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

// criticalPatterns 是「需人工介入」的资金/结算事件标记（大小写不敏感子串匹配）。
// 命中任一 + level>=Error 才告警——普通 Error（可自愈/重试）不打扰值班。
// 取自当前代码里真实存在的高危 log.Error 消息，新增高危点沿用这些措辞即自动覆盖。
var criticalPatterns = []string{
	"SETTLE FAILED",                  // relay 6 路径同步结算失败（audio/embeddings/images/images_edit/relay/rerank）
	"DRIFT DETECTED",                 // 账本漂移自检检出（有人绕开 ledger 写余额 / 局部写损坏）
	"drift check failed",             // 漂移巡检本身失败
	"manual reconcile",              // 需人工对账
	"manual credit",                 // 需人工补入账
	"CREDIT AND PROOF-PERSIST FAILED", // x402 链上已扣但入账+存证双失败（最危险）
	"provider_ref persist failed",   // 支付单外部引用落库失败 → callback 漏发即盲区
	"reconcile failed",              // 充值对账 job 失败
	"credit failed",                 // 入账失败
}

// Handler 装饰一个 inner slog.Handler：正常输出照旧，命中 critical 的 Error 旁路告警。
type Handler struct {
	inner   slog.Handler
	webhook string
	client  *http.Client
	dedup   *deduper
}

// NewHandler 包装 inner。webhook 从 LAIAI_ALERT_WEBHOOK 读取（空则告警旁路静默）。
// 有意不依赖 config 包——root logger 必须先于 config.Load 构造（config 失败也要能 log）。
func NewHandler(inner slog.Handler) *Handler {
	return &Handler{
		inner:   inner,
		webhook: strings.TrimSpace(os.Getenv("LAIAI_ALERT_WEBHOOK")),
		client:  &http.Client{Timeout: 3 * time.Second},
		dedup:   newDeduper(5 * time.Minute),
	}
}

func (h *Handler) Enabled(ctx context.Context, l slog.Level) bool { return h.inner.Enabled(ctx, l) }

func (h *Handler) Handle(ctx context.Context, r slog.Record) error {
	// 日志正常输出永远先行且不受告警影响——告警是纯旁路。
	err := h.inner.Handle(ctx, r)
	if h.webhook != "" && r.Level >= slog.LevelError && isCritical(r.Message) {
		h.fire(r)
	}
	return err
}

func (h *Handler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &Handler{inner: h.inner.WithAttrs(attrs), webhook: h.webhook, client: h.client, dedup: h.dedup}
}

func (h *Handler) WithGroup(name string) slog.Handler {
	return &Handler{inner: h.inner.WithGroup(name), webhook: h.webhook, client: h.client, dedup: h.dedup}
}

func isCritical(msg string) bool {
	up := strings.ToUpper(msg)
	for _, p := range criticalPatterns {
		if strings.Contains(up, strings.ToUpper(p)) {
			return true
		}
	}
	return false
}

func (h *Handler) fire(r slog.Record) {
	// 同一消息窗口内去重（drift 每小时巡检、SETTLE FAILED 可能突发多条）。
	if !h.dedup.allow(r.Message) {
		return
	}

	var b strings.Builder
	b.WriteString("🚨 LAIAI 资金告警\n")
	b.WriteString(r.Message)
	b.WriteByte('\n')
	r.Attrs(func(a slog.Attr) bool {
		b.WriteString(a.Key)
		b.WriteByte('=')
		b.WriteString(a.Value.String())
		b.WriteString("  ")
		return true
	})
	text := b.String()

	go func() {
		defer func() { _ = recover() }() // 告警绝不影响业务路径
		payload, _ := json.Marshal(map[string]any{
			"msg_type": "text",
			"content":  map[string]string{"text": text},
		})
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, h.webhook, bytes.NewReader(payload))
		if err != nil {
			return
		}
		req.Header.Set("Content-Type", "application/json")
		resp, err := h.client.Do(req)
		if err != nil {
			return
		}
		_ = resp.Body.Close()
	}()
}

// deduper 时间窗去重：同 key 在 window 内只放行一次。
type deduper struct {
	mu     sync.Mutex
	seen   map[string]time.Time
	window time.Duration
	now    func() time.Time
}

func newDeduper(w time.Duration) *deduper {
	return &deduper{seen: map[string]time.Time{}, window: w, now: time.Now}
}

func (d *deduper) allow(key string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	now := d.now()
	if t, ok := d.seen[key]; ok && now.Sub(t) < d.window {
		return false
	}
	d.seen[key] = now
	if len(d.seen) > 256 { // 防无界增长：超阈值时顺手清过期条目
		for k, t := range d.seen {
			if now.Sub(t) >= d.window {
				delete(d.seen, k)
			}
		}
	}
	return true
}
