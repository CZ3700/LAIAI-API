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
	OK              bool           `json:"ok"`
	DetectedBaseURL string         `json:"detected_base_url"`
	Models          []string       `json:"models"`
	Tried           []ProbeAttempt `json:"tried"`
	Gateway         string         `json:"gateway"` // 上游网关线索（server/via 头）
	Message         string         `json:"message"`
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
// 已含 /v1 等版本段则只用原值；否则按常见格式补全。
func candidateBaseURLs(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if !strings.HasPrefix(raw, "http://") && !strings.HasPrefix(raw, "https://") {
		raw = "https://" + raw
	}
	raw = strings.TrimRight(raw, "/")
	if strings.Contains(raw, "/v1") || strings.Contains(raw, "/v1beta") {
		return []string{raw}
	}
	return []string{raw + "/v1", raw, raw + "/openai/v1"}
}

// ProbeChannel：用 key + 地址试探正确 base_url（按候选顺序 GET /models），
// 返回首个返回合法模型列表的格式 + 可调模型名。纯探测，不落库不计费。
func (r *Relay) ProbeChannel(ctx context.Context, apiKey, rawURL string) *ProbeResult {
	res := &ProbeResult{}
	candidates := candidateBaseURLs(rawURL)
	if len(candidates) == 0 {
		res.Message = "base_url is required"
		return res
	}
	for _, base := range candidates {
		attempt := ProbeAttempt{BaseURL: base}
		models, gw, status, err := r.fetchModels(ctx, base, apiKey)
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
		res.Gateway = gw
		return res
	}
	res.Message = "no base_url variant returned a valid model list — check the key and domain"
	return res
}

// ProbeExistingChannel：对已落库渠道拉取上游模型列表（gap P2-D，自动拉模型/缺失检测用）。
// 解密渠道存储的凭证后复用 ProbeChannel（凭证处理留在 relay 内，与 TestChannel 同口径）。
func (r *Relay) ProbeExistingChannel(ctx context.Context, ch *Channel) *ProbeResult {
	cred, err := r.Keyring.Decrypt(ch.CredentialEnc)
	if err != nil {
		return &ProbeResult{Message: "credential decrypt failed"}
	}
	return r.ProbeChannel(ctx, cred, ch.BaseURL)
}

// fetchModels：GET {base}/models（OpenAI 兼容），解析 {data:[{id}]}。
func (r *Relay) fetchModels(ctx context.Context, base, apiKey string) (models []string, gateway string, status int, err error) {
	ctx, cancel := context.WithTimeout(ctx, probePerCandidateTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", nil)
	if err != nil {
		return nil, "", 0, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := r.Client.Do(req)
	if err != nil {
		return nil, "", 0, fmt.Errorf("unreachable")
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	gateway = strings.TrimSpace(resp.Header.Get("Server"))
	if via := resp.Header.Get("Via"); via != "" {
		gateway = strings.TrimSpace(gateway + " " + via)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, gateway, resp.StatusCode, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	if !IsLLMContentType(resp.Header.Get("Content-Type")) {
		return nil, gateway, resp.StatusCode, fmt.Errorf("non-JSON (wrong path?)")
	}
	var parsed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if jsonErr := json.Unmarshal(body, &parsed); jsonErr != nil {
		return nil, gateway, resp.StatusCode, fmt.Errorf("unparseable model list")
	}
	for _, m := range parsed.Data {
		if m.ID != "" {
			models = append(models, m.ID)
		}
	}
	if len(models) == 0 {
		return nil, gateway, resp.StatusCode, fmt.Errorf("empty model list")
	}
	return models, gateway, resp.StatusCode, nil
}
