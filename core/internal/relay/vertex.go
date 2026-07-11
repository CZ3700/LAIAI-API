package relay

// Google Vertex AI 适配器（丝滑度批 C）。Vertex 把多家模型托管在同一鉴权/端点体系下：
//   - Gemini 模型  → 原生 Gemini generateContent（复用 geminiRequestBody + geminiAdapter 响应侧）
//   - Claude 模型  → Anthropic Messages rawPredict（复用 anthropicRequestBody + anthropicAdapter
//     响应侧；Vertex 方言：删 model/stream 字段、加 anthropic_version=vertex-2023-10-16）
// 鉴权统一 GCP service account → OAuth2 Bearer（gcpauth.go）。
//
// 凭证格式：`region|<service-account-json>`（project 从 SA JSON 的 project_id 读）。
// 端点 https://{region}-aiplatform.googleapis.com 按 region 自动构造（base_url 留空即可，
// 填了则作为 override，用于私有/代理端点）。

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

const vertexAnthropicVersion = "vertex-2023-10-16"

// respUpstreamModelKey：relay 在 c.Set 里存**上游** model 名（经 UpstreamModel 映射后的名）。
// Vertex 响应侧据此选 anthropic/gemini 解析器，与 buildRequest 的 publisher 判定同源——
// 避免渠道做了跨 claude/gemini 边界的 model 重命名时，请求体按上游名走 A、响应却按对外名按 B 解析。
const respUpstreamModelKey = "laiai_resp_upstream_model"

type vertexAdapter struct{}

// parseVertexCred 拆 `region|<sa_json>`。SA JSON 本身不含裸 '|'（private_key PEM 用 \n 转义、
// 各字段值无管道符），用首个 '|' 切分安全。
func parseVertexCred(credential string) (region string, sa *gcpServiceAccount, err error) {
	region, saJSON, ok := strings.Cut(credential, "|")
	region = strings.TrimSpace(region)
	if !ok || region == "" || strings.TrimSpace(saJSON) == "" {
		return "", nil, fmt.Errorf("vertex: 凭证需 region|<service-account-json> 两段")
	}
	sa, err = parseGCPServiceAccount(saJSON)
	if err != nil {
		return "", nil, err
	}
	return region, sa, nil
}

// vertexIsClaude：模型名归属 anthropic publisher（否则默认 google/Gemini）。
func vertexIsClaude(model string) bool {
	return strings.HasPrefix(strings.ToLower(model), "claude")
}

// vertexClaudeFromCtx：响应侧判 publisher，优先上游名（与 buildRequest 同源），回退对外名。
func vertexClaudeFromCtx(c *gin.Context) bool {
	m := c.GetString(respUpstreamModelKey)
	if m == "" {
		m = c.GetString(respModelKey)
	}
	return vertexIsClaude(m)
}

func (vertexAdapter) buildRequest(ctx context.Context, ch *Channel, credential string, req map[string]any, stream bool) (*http.Request, error) {
	model, _ := req["model"].(string)
	if model == "" {
		return nil, fmt.Errorf("vertex: 缺 model")
	}
	region, sa, err := parseVertexCred(credential)
	if err != nil {
		return nil, err
	}

	base := strings.TrimRight(ch.BaseURL, "/")
	if base == "" {
		base = "https://" + region + "-aiplatform.googleapis.com"
	}

	var out map[string]any
	var publisher, suffix string
	if vertexIsClaude(model) {
		out, err = anthropicRequestBody(req, stream)
		if err != nil {
			return nil, err
		}
		delete(out, "model")  // Vertex Claude：model 在 URL，body 不带
		delete(out, "stream") // 流式由端点（streamRawPredict）决定
		out["anthropic_version"] = vertexAnthropicVersion
		publisher, suffix = "anthropic", "rawPredict"
		if stream {
			suffix = "streamRawPredict?alt=sse"
		}
	} else {
		out, err = geminiRequestBody(req)
		if err != nil {
			return nil, err
		}
		publisher, suffix = "google", "generateContent"
		if stream {
			suffix = "streamGenerateContent?alt=sse"
		}
	}

	token, err := gcpAccessToken(ctx, sa, time.Now())
	if err != nil {
		return nil, err
	}
	body, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	endpoint := fmt.Sprintf("%s/v1/projects/%s/locations/%s/publishers/%s/models/%s:%s",
		base, sa.ProjectID, region, publisher, model, suffix)
	upReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	upReq.Header.Set("Content-Type", "application/json")
	upReq.Header.Set("Authorization", "Bearer "+token)
	return upReq, nil
}

// pipeJSON/pipeSSE 委托：Vertex 回的就是原生 Gemini/Anthropic 响应体，按上游 model 名选解析器。
func (vertexAdapter) pipeJSON(c *gin.Context, body io.Reader) (usage, int, error) {
	if vertexClaudeFromCtx(c) {
		return anthropicAdapter{}.pipeJSON(c, body)
	}
	return geminiAdapter{}.pipeJSON(c, body)
}

func (vertexAdapter) pipeSSE(c *gin.Context, body io.Reader) (usage, int, error) {
	if vertexClaudeFromCtx(c) {
		return anthropicAdapter{}.pipeSSE(c, body)
	}
	return geminiAdapter{}.pipeSSE(c, body)
}
