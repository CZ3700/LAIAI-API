package relay

// Diagnostics Key 检测（§4.8，批次 7）：用用户提交的 key 探测某厂商的有效性/延迟/可用模型。
//
// 安全（红线 7 + SSRF）：
//   - 被测 key **只在内存中用于本次探测，绝不落库、绝不进日志**（同步无落盘 = 零泄漏面）。
//   - 恒经 `guardedClient(false)` 拨号守卫：解析后真实 IP 复检（DNS-rebinding-safe），metadata 恒拦，
//     **绝不放行私网**（不吃 advanced_custom_allow_private 开关，与 BYOK 同纪律——防租户探测平台内网）。
//   - provider 走白名单（diagAllowedProvider）；custom base_url 由 server 层强制 https + ValidateAdvancedUpstreamURL。
//   - fetchModels 错误路径本就只返 "unreachable"/"HTTP %d"（不回显 key）——复用它，不新造回显面。

import (
	"context"
	"fmt"
	"time"
)

// diagPerCandidateTimeout：诊断单候选超时（比渠道 probe 的 12s 短——用户交互场景，快速失败）。
const diagPerCandidateTimeout = 8 * time.Second

// diagMaxModelsSample：报告里回样例模型的上限（完整列表可能上百，UI 只需样例 + 总数）。
const diagMaxModelsSample = 24

// DiagResult：Key 检测报告。**绝不含被测 key**。
type DiagResult struct {
	OK           bool     `json:"ok"`
	Status       string   `json:"status"`         // ok | invalid_key | unreachable | forbidden | error
	BaseURL      string   `json:"base_url"`       // 探测命中的 base_url（命中时）
	LatencyMs    int      `json:"latency_ms"`     // 命中候选的往返耗时
	ModelCount   int      `json:"model_count"`    // 可用模型总数
	ModelsSample []string `json:"models_sample"`  // 样例模型（截断至 diagMaxModelsSample）
	Gateway      string   `json:"gateway"`        // 上游网关线索（Server/Via 头）
	Message      string   `json:"message"`        // 人类可读结论（绝不含 key）
}

// diagAllowedProvider：Diagnostics 允许探测的 provider 白名单——已知云端 chat 家族。
// 与 BYOK 白名单同集（一致的安全姿态）；custom base_url 由 server 层守卫。
func diagAllowedProvider(provider string) bool {
	return byokProviders[provider]
}

// CheckKey：同步探测 key 有效性/延迟/模型列表。key 只在内存用于本次探测（红线 7）。
// rawURL 空 → 用 provider 默认 base_url；非空由 server 层已校验 https + SSRF。
// 恒经 guardedClient(false)（绝不放行私网，不吃运行时开关）。
func (r *Relay) CheckKey(ctx context.Context, apiKey, rawURL, provider string) *DiagResult {
	if !diagAllowedProvider(provider) {
		return &DiagResult{Status: "error", Message: "provider not supported for diagnostics"}
	}
	base := rawURL
	if base == "" {
		if p, ok := ProfileFor(provider); ok {
			base = p.DefaultBaseURL
		}
	}
	candidates := candidateBaseURLs(base)
	if len(candidates) == 0 {
		return &DiagResult{Status: "error", Message: "base_url is required for this provider"}
	}
	cl := r.guardedClient(ByokAllowPrivateForTest) // 生产恒 false（变量仅测试可设，与 BYOK 同）
	family := probeFamily(provider)
	custom := rawURL != "" // custom base_url 时收敛可探测面（对抗 review：不回 Gateway banner）

	var lastStatus int
	for _, cand := range candidates {
		start := r.Now()
		models, gw, status, err := r.fetchModels(ctx, cl, cand, apiKey, family)
		lat := int(r.Now().Sub(start).Milliseconds())
		if err != nil {
			if status != 0 {
				lastStatus = status
			}
			continue // 换下一个候选 base_url
		}
		sample := models
		if len(sample) > diagMaxModelsSample {
			sample = sample[:diagMaxModelsSample]
		}
		// Gateway banner 仅在用 provider 默认端点时回显（对抗 review：custom base_url 时 Gateway=
		// Server/Via 头是对任意公网 HTTPS 目标的侦察面，置空消除）。
		if custom {
			gw = ""
		}
		return &DiagResult{
			OK: true, Status: "ok", BaseURL: cand, LatencyMs: lat,
			ModelCount: len(models), ModelsSample: sample, Gateway: gw,
			Message: fmt.Sprintf("valid — %d models available", len(models)),
		}
	}
	// 全候选失败：映射为**粗粒度语义类**结论（对抗 review：绝不回显逐字 HTTP 状态码/上游响应体/key——
	// 逐字状态码把诊断接口变成对任意公网 HTTPS 目标的可区分状态 oracle）。
	return diagFailResult(lastStatus)
}

// diagFailResult：上游失败 → 粗粒度语义类（invalid_key/forbidden/unreachable/error）。
// **不含逐字状态码**：只区分「key 被拒（401/403，对合法用户有诊断价值）」与「其它（一律通用）」，
// 消除对任意公网端点的精确状态回显 oracle。
func diagFailResult(status int) *DiagResult {
	switch status {
	case 401:
		return &DiagResult{Status: "invalid_key", Message: "the key was rejected — check it is correct and has access"}
	case 403:
		return &DiagResult{Status: "forbidden", Message: "the key was rejected — check it has access to this endpoint"}
	case 0:
		return &DiagResult{Status: "unreachable", Message: "could not reach the endpoint — check the base URL and network"}
	default:
		// 404/429/5xx/其它：一律通用，不回显状态码，不暴露端点是否存活/响应类型
		return &DiagResult{Status: "error", Message: "could not validate the key against this endpoint — check the provider and base URL"}
	}
}
