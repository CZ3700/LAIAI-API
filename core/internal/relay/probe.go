package relay

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// ProbeResult：渠道探测结果（admin 上货前自检）。
type ProbeResult struct {
	OK              bool              `json:"ok"`
	DetectedBaseURL string            `json:"detected_base_url"`
	Models          []string          `json:"models"`
	ModelOwners     map[string]string `json:"model_owners,omitempty"`
	Tried           []ProbeAttempt    `json:"tried"`
	Gateway         string            `json:"gateway"` // 上游网关线索（server/via 头）
	Message         string            `json:"message"`
}

// ProbeAttempt：单个候选 base_url 的尝试结果。
type ProbeAttempt struct {
	BaseURL string `json:"base_url"`
	Status  int    `json:"status"`
	OK      bool   `json:"ok"`
	Note    string `json:"note"`
}

const probePerCandidateTimeout = 12 * time.Second

// candidateBaseURLs：从用户给的原始地址（可能只是主域名）推导候选 base_url。
// 已含 /v1 等版本段则只用原值；否则按常见网关前缀约定逐个试探：
//
//	{host}/v1        OpenAI 官方与多数兼容网关
//	{host}/api/v1    MaaS/自建聚合站把 OpenAI 兼容面挂在 /api 下（真实案例：仅给主域名时
//	                 此前三个候选全 404，真实端点 /api/v1/models 从未被试到）
//	{host}           用户给的已是完整 base（网关直接在根提供 /models）
//	{host}/openai/v1 部分聚合器/Azure 风格前缀
func candidateBaseURLs(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		raw = "https://" + raw
	}
	raw = strings.TrimRight(raw, "/")
	if strings.Contains(raw, "/v1") || strings.Contains(raw, "/v1beta") || strings.Contains(raw, "/api/v3") {
		return []string{raw}
	}
	if strings.HasSuffix(raw, "/api") { // 用户已给到 /api 时首候选即 /api/v1，不拼出 /api/api/v1
		return []string{raw + "/v1", raw + "/v3", raw, raw + "/openai/v1"}
	}
	return []string{raw + "/v1", raw + "/api/v1", raw + "/api/v3", raw + "/v1beta", raw, raw + "/openai/v1"}
}

// probeFamily：把渠道 provider 归一到协议族（anthropic/gemini/openai），决定 /models 的
// 鉴权方式与响应解析口径（P2-10：provider-aware probe）。未登记按 OpenAI 兼容。
func probeFamily(provider string) string {
	if p, ok := ProfileFor(provider); ok {
		switch p.Adapter {
		case "anthropic", "gemini":
			return p.Adapter
		}
	}
	return "openai"
}

// ProbeChannel：用 key + 地址 + provider 试探正确 base_url（按候选顺序 GET /models，
// 按协议族选鉴权/解析），返回首个合法模型列表 + 可调模型名。纯探测，不落库不计费。
// guarded=true（advanced_custom）时经 SSRF 拨号守卫（对抗 review：probe 曾用裸 r.Client，
// 绕过 SSRF 边界可打内网/metadata 并回显响应）。
func (r *Relay) ProbeChannel(ctx context.Context, apiKey, rawURL, provider string, guarded bool) *ProbeResult {
	res := &ProbeResult{}
	candidates := candidateBaseURLs(rawURL)
	if len(candidates) == 0 {
		res.Message = "base_url is required"
		return res
	}
	cl := r.Client
	if guarded {
		cl = r.guardedClient(r.advancedAllowPrivate())
	}
	family := probeFamily(provider)
	for _, base := range candidates {
		attempt := ProbeAttempt{BaseURL: base}
		models, owners, gw, status, err := r.fetchModelsWithOwners(ctx, cl, base, apiKey, family)
		attempt.Status = status
		if err != nil {
			attempt.Note = err.Error()
			res.Tried = append(res.Tried, attempt)
			continue
		}
		attempt.OK = true
		attempt.Note = fmt.Sprintf("%d models", len(models))
		res.Tried = append(res.Tried, attempt)
		res.OK = true
		res.DetectedBaseURL = base
		res.Models = models
		res.ModelOwners = owners
		res.Gateway = gw
		return res
	}
	res.Message = "no base_url variant returned a valid model list — check the key and domain"
	return res
}

// ProbeExistingChannel：对已落库渠道拉取上游模型列表（gap P2-D，自动拉模型/缺失检测用）。
// 解密渠道存储的凭证后复用 ProbeChannel（凭证处理留在 relay 内，与 TestChannel 同口径）。
// guarded 随渠道类型（advanced_custom 才守卫），与数据面信任口径一致。
func (r *Relay) ProbeExistingChannel(ctx context.Context, ch *Channel) *ProbeResult {
	return r.probeExisting(ctx, ch, ch.isAdvancedCustom())
}

// ProbeExistingChannelGuarded：后台无人值守自动同步（§3.10 SyncChannelModels）专用——
// 无论渠道类型一律经 SSRF 拨号守卫（红线 5：周期 job 不得被 admin 误配的内网/metadata base_url
// 牵着打内网；operator 的合法内网上游用 advanced_custom_allow_private 逃生阀放行）。
func (r *Relay) ProbeExistingChannelGuarded(ctx context.Context, ch *Channel) *ProbeResult {
	return r.probeExisting(ctx, ch, true)
}

func (r *Relay) probeExisting(ctx context.Context, ch *Channel, guarded bool) *ProbeResult {
	_, credEnc, credOK := r.selectCredential(ch) // 选 key（0034 Multi-key；probe 诊断不禁用）
	if !credOK {
		return &ProbeResult{Message: "channel has no usable credential"}
	}
	cred, err := r.Keyring.Decrypt(credEnc)
	if err != nil {
		return &ProbeResult{Message: "credential decrypt failed"}
	}
	return r.ProbeChannel(ctx, cred, ch.BaseURL, ch.Provider, guarded)
}

// fetchModels：GET {base}/models 拉模型列表，按协议族选鉴权头与响应解析（P2-10）：
//
//	openai    Authorization: Bearer；解析 {data:[{id,owned_by}]}
//	anthropic x-api-key + anthropic-version；解析 {data:[{id}]}
//	gemini    ?key= 查询参数；解析 {models:[{name:"models/x"}]}（去 models/ 前缀）
func (r *Relay) fetchModels(ctx context.Context, cl *http.Client, base, apiKey, family string) (models []string, gateway string, status int, err error) {
	models, _, gateway, status, err = r.fetchModelsWithOwners(ctx, cl, base, apiKey, family)
	return models, gateway, status, err
}

func (r *Relay) fetchModelsWithOwners(ctx context.Context, cl *http.Client, base, apiKey, family string) (models []string, owners map[string]string, gateway string, status int, err error) {
	ctx, cancel := context.WithTimeout(ctx, probePerCandidateTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", nil)
	if err != nil {
		return nil, nil, "", 0, err
	}
	if key := strings.TrimSpace(apiKey); key != "" {
		switch family {
		case "anthropic":
			req.Header.Set("x-api-key", key)
			req.Header.Set("anthropic-version", "2023-06-01")
		case "gemini":
			q := req.URL.Query()
			q.Set("key", key)
			req.URL.RawQuery = q.Encode()
		default:
			req.Header.Set("Authorization", "Bearer "+key)
		}
	}
	resp, err := cl.Do(req)
	if err != nil {
		return nil, nil, "", 0, fmt.Errorf("unreachable")
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	gateway = strings.TrimSpace(resp.Header.Get("Server"))
	if via := resp.Header.Get("Via"); via != "" {
		gateway = strings.TrimSpace(gateway + " " + via)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, nil, gateway, resp.StatusCode, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if !IsLLMContentType(resp.Header.Get("Content-Type")) {
		return nil, nil, gateway, resp.StatusCode, fmt.Errorf("non-JSON (wrong path?)")
	}
	models, owners, perr := parseModelListWithOwners(family, body)
	if perr != nil {
		return nil, nil, gateway, resp.StatusCode, perr
	}
	if len(models) == 0 {
		return nil, nil, gateway, resp.StatusCode, fmt.Errorf("empty model list")
	}
	return models, owners, gateway, resp.StatusCode, nil
}

func isGenericModelOwner(owner string) bool {
	switch strings.ToLower(strings.TrimSpace(owner)) {
	case "custom", "system", "user", "new-api", "one-api", "openai-internal", "organization-owner":
		return true
	default:
		return false
	}
}

// parseModelList 按协议族解析 /models 响应。gemini 形如 {models:[{name:"models/x"}]}，
// 其余形如 {data:[{id:"x"}]}。
func parseModelList(family string, body []byte) ([]string, error) {
	models, _, err := parseModelListWithOwners(family, body)
	return models, err
}

func parseModelListWithOwners(family string, body []byte) ([]string, map[string]string, error) {
	var models []string
	if family == "gemini" {
		var parsed struct {
			Models []struct {
				Name string `json:"name"`
			} `json:"models"`
		}
		if err := json.Unmarshal(body, &parsed); err != nil {
			return nil, nil, fmt.Errorf("unparseable model list")
		}
		for _, m := range parsed.Models {
			if id := strings.TrimPrefix(m.Name, "models/"); id != "" {
				models = append(models, id)
			}
		}
		return models, nil, nil
	}
	var parsed struct {
		Data []struct {
			ID      string `json:"id"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, nil, fmt.Errorf("unparseable model list")
	}
	owners := make(map[string]string)
	for _, m := range parsed.Data {
		if m.ID != "" {
			models = append(models, m.ID)
			if ob := strings.TrimSpace(m.OwnedBy); ob != "" && !isGenericModelOwner(ob) {
				owners[m.ID] = ob
			}
		}
	}
	return models, owners, nil
}
