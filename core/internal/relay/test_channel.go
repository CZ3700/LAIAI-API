package relay

import (
	"context"
	"io"
	"strings"
	"time"
)

// ChannelTestResult：渠道连通性测试结果（admin 一键测试）。
type ChannelTestResult struct {
	OK        bool   `json:"ok"`
	Status    int    `json:"status"`     // 上游 HTTP 状态（0=不可达）
	LatencyMs int64  `json:"latency_ms"` // 发起 → 读完响应
	Model     string `json:"model"`      // 实测对外模型名
	Message   string `json:"message"`    // 失败原因（含上游错误摘要，admin 自测自己的渠道，可回显）
}

const channelTestTimeout = 30 * time.Second

// TestChannel 用最小真实请求测试渠道连通性：1 条 user 消息 + max_tokens=16。
// 不计费、不写调用记录、不触发自动禁用——纯诊断。禁用中的渠道也可测（修好后再启用）。
func (r *Relay) TestChannel(ctx context.Context, ch *Channel, model string) *ChannelTestResult {
	res := &ChannelTestResult{Model: model}
	credential, err := r.Keyring.Decrypt(ch.CredentialEnc)
	if err != nil {
		res.Message = "channel credential decrypt failed (key rotation issue?)"
		return res
	}
	req := map[string]any{
		"model":      ch.UpstreamModel(model),
		"messages":   []map[string]any{{"role": "user", "content": "ping"}},
		"max_tokens": float64(16),
	}
	ctx, cancel := context.WithTimeout(ctx, channelTestTimeout)
	defer cancel()
	// override 与生产路径同款应用（对抗 review）：依赖自定义认证头/方言字段的渠道，
	// 探针必须走同一改写，否则测试恒假阴性（生产通、探针 401）。
	upReq, err := adapterFor(ch.Provider).buildRequest(ctx, ch, credential,
		applyParamOverride(req, ch.ParamOverride), false)
	if err != nil {
		res.Message = "request build failed: " + err.Error()
		return res
	}
	applyHeaderOverride(upReq, ch.HeaderOverride, credential)
	t0 := time.Now()
	resp, err := r.clientFor(ch).Do(upReq)
	if err != nil {
		res.LatencyMs = time.Since(t0).Milliseconds()
		res.Message = "upstream unreachable: " + err.Error()
		return res
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
	res.LatencyMs = time.Since(t0).Milliseconds()
	res.Status = resp.StatusCode

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		res.Message = "upstream error: " + excerpt(body, 200)
		return res
	}
	if !IsLLMContentType(resp.Header.Get("Content-Type")) {
		res.Message = "upstream returned non-LLM response (content-type " +
			resp.Header.Get("Content-Type") + ") — check base_url (missing /v1?)"
		return res
	}
	res.OK = true
	return res
}

// excerpt：截断 + 压平换行，供单行展示。
func excerpt(b []byte, n int) string {
	s := strings.Join(strings.Fields(string(b)), " ")
	if len(s) > n {
		s = s[:n] + "…"
	}
	return s
}
