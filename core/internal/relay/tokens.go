package relay

import (
	"encoding/json"
	"sync"

	"github.com/pkoukk/tiktoken-go"
	tiktoken_loader "github.com/pkoukk/tiktoken-go-loader"
)

// tiktoken 精确化输入估算（知识库 §4.2，§7 P1-5）：
//   - cl100k_base 单 codec（NewAPI 同款默认），离线词表内嵌——服务进程禁止运行时下载依赖
//   - 仅用于 hold 预冻结的精度提升；capture 永远以上游真实 usage 为准
//   - 非 OpenAI 系模型 cl100k 不精确，但远好于 chars/4；剩余偏差由 overage 机制兜底
var (
	encOnce sync.Once
	enc     *tiktoken.Tiktoken
)

func encoder() *tiktoken.Tiktoken {
	encOnce.Do(func() {
		tiktoken.SetBpeLoader(tiktoken_loader.NewOfflineLoader())
		e, err := tiktoken.GetEncoding("cl100k_base")
		if err == nil {
			enc = e
		}
	})
	return enc
}

func countText(e *tiktoken.Tiktoken, s string) int64 {
	return int64(len(e.Encode(s, nil, nil)))
}

// CountChatTokens 对统一格式 chat 请求做输入 token 估算。
// 计数模型（OpenAI chat 计数惯例）：每消息 +4 开销（分隔符/role 框架）+ role + content；
// 末尾 +3 assistant priming；tools 定义按 JSON 文本计入（它们真实占用 prompt）。
// 任何解析失败 → 回退 chars/4（EstimateTokens），绝不因估算失败拒绝请求。
func CountChatTokens(req map[string]any, rawLen int) int64 {
	e := encoder()
	if e == nil {
		return EstimateTokens(rawLen)
	}
	msgs, ok := req["messages"].([]any)
	if !ok || len(msgs) == 0 {
		return EstimateTokens(rawLen)
	}
	total := int64(3) // assistant priming
	for _, rm := range msgs {
		m, ok := rm.(map[string]any)
		if !ok {
			continue
		}
		total += 4
		if role, ok := m["role"].(string); ok {
			total += countText(e, role)
		}
		switch content := m["content"].(type) {
		case string:
			total += countText(e, content)
		case nil:
			// assistant tool_calls 消息可能无 content
		default:
			// 多模态/结构化 content：按 JSON 文本近似（图片 token 模型 M2 Task/vision 时引入）
			if b, err := json.Marshal(content); err == nil {
				total += EstimateTokens(len(b))
			}
		}
		if tcs, ok := m["tool_calls"]; ok {
			if b, err := json.Marshal(tcs); err == nil {
				total += countText(e, string(b))
			}
		}
	}
	if tools, ok := req["tools"]; ok {
		if b, err := json.Marshal(tools); err == nil {
			total += countText(e, string(b))
		}
	}
	if total < 16 {
		total = 16
	}
	return total
}
