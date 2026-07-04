package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
)

// azureAdapter：Azure OpenAI 方言（知识库 §5/§7）。
// 与 OpenAI 标准的差异只有两处：URL 走 deployments 路径 + 认证用 api-key 头。
// model 即 deployment 名（Azure 侧自定义）；响应/流式与 OpenAI 完全一致，pipe 复用。
type azureAdapter struct{ openaiAdapter }

// AzureAPIVersion：默认 api-version；需要更新时改这里（渠道级覆盖留待 channel 配置扩展）。
const AzureAPIVersion = "2024-08-01-preview"

func (azureAdapter) buildRequest(ctx context.Context, ch *Channel, credential string, req map[string]any, stream bool) (*http.Request, error) {
	if stream {
		ensureIncludeUsage(req) // 保留 override/客户端兄弟字段，强制 include_usage（对抗 review）
	}
	model, _ := req["model"].(string)
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	url := strings.TrimRight(ch.BaseURL, "/") +
		"/openai/deployments/" + model + "/chat/completions?api-version=" + AzureAPIVersion
	upReq, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	upReq.Header.Set("Content-Type", "application/json")
	upReq.Header.Set("api-key", credential) // Azure：api-key 头，不是 Authorization（知识库 §6 头部速查）
	return upReq, nil
}

// volcengineChatAdapter：字节豆包/方舟 chat（知识库 §6.1）。
// OpenAI 兼容协议，唯一方言点：`bot-` 前缀模型路由到 /bots/chat/completions。
// （任务型 Seedance 走 internal/task 的 volcengineTask，与本适配器分工。）
type volcengineChatAdapter struct{ openaiAdapter }

func (volcengineChatAdapter) buildRequest(ctx context.Context, ch *Channel, credential string, req map[string]any, stream bool) (*http.Request, error) {
	if stream {
		ensureIncludeUsage(req) // 保留 override/客户端兄弟字段，强制 include_usage（对抗 review）
	}
	model, _ := req["model"].(string)
	path := "/chat/completions"
	if strings.HasPrefix(model, "bot-") {
		path = "/bots/chat/completions"
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	upReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(ch.BaseURL, "/")+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	upReq.Header.Set("Content-Type", "application/json")
	upReq.Header.Set("Authorization", "Bearer "+credential)
	return upReq, nil
}
