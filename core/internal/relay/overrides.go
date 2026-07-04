package relay

// 渠道级 Header/Param Override（商用迭代指令 §3.4；设计 §4.4）：运营不写代码接入"有方言的上游"。
//   - param_override：进 adapter 前对请求体深合并（值 null=删字段，嵌套亦然；对象递归合并）。
//     应用在每次 attempt 的副本上——不同渠道不同 override，绝不跨渠道残留、绝不改动原请求。
//   - header_override：adapter 默认头之后应用（运营优先级最高）；值 null=删头；
//     {{credential}} 占位符注入渠道解密凭证（自定义认证头不落明文 key，红线 7）。
//   - 受保护面：param 的资金钳制键写入时拒绝、应用时防御性跳过（override 抬高输出上限会绕过
//     hold 估算钳制 → 负余额风险）；header 的 Host/Content-Length 禁改（破坏 HTTP 语义）。

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"sort"
	"strings"
)

// protectedParamKeys：param_override 永不允许触碰的顶层键。
// 资金面：输出上限/候选数/思考预算参与 hold 估算钳制（relay.go P0-2 系列），override 若在
// 钳制后抬高会让上游产出 > hold → overage 刷负余额。含方言别名（对抗 review：openai 系
// adapter 整 map 透传，TGI 的 max_new_tokens / Ollama 的 num_predict / vLLM 的 best_of /
// Responses 的 max_output_tokens 等别名同样能抬高上游产出，与 outputCapAliases 同源维护）。
// 路由面：model 由模型映射专管；stream 决定管道选择；messages 是用户内容不容运营改写。
var protectedParamKeys = map[string]bool{
	"model": true, "stream": true, "messages": true,
	"max_tokens": true, "max_completion_tokens": true, "n": true, "thinking": true,
	"max_output_tokens": true, "max_new_tokens": true, "num_predict": true,
	"max_tokens_to_sample": true, "best_of": true,
}

// forbiddenHeaderKeys：header_override 禁改头（canonical 形态比较）。
var forbiddenHeaderKeys = map[string]bool{
	"Host": true, "Content-Length": true,
}

// authHeaderKeys：认证类头（canonical 形态）。值必须为 null 或含 {{credential}} 占位符——
// 否则运营会把上游 key 明文粘进 header_override（明文落库 + list 回显，红线 7 误用路径，
// 对抗 review）。真实凭证永远走渠道 api_key 字段（信封加密），头里只放占位符。
var authHeaderKeys = map[string]bool{
	"Authorization": true, "Proxy-Authorization": true,
	"X-Api-Key": true, "Api-Key": true, "X-Auth-Token": true,
	"X-Goog-Api-Key": true, "X-Access-Token": true,
}

// credentialPlaceholder：header_override 值中的凭证占位符。
const credentialPlaceholder = "{{credential}}"

// ValidateParamOverride：admin 写入前校验（apply 侧另有防御性跳过）。空串 = 无 override，合法。
func ValidateParamOverride(s string) error {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return fmt.Errorf("param_override must be a JSON object")
	}
	for k := range m {
		if protectedParamKeys[k] {
			return fmt.Errorf("param_override key %q is protected (billing/routing clamp)", k)
		}
	}
	return nil
}

// ValidateHeaderOverride：admin 写入前校验。值必须是 string 或 null；
// 认证类头必须用 {{credential}} 占位符（真实凭证走渠道 api_key 字段信封加密）；
// 值禁含控制字符（否则过校验后在 net/http transport 层才炸，错误被误分类为上游不可达）。
func ValidateHeaderOverride(s string) error {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return fmt.Errorf("header_override must be a JSON object")
	}
	for k, v := range m {
		canon := http.CanonicalHeaderKey(k)
		if forbiddenHeaderKeys[canon] {
			return fmt.Errorf("header_override key %q is forbidden (breaks HTTP semantics)", k)
		}
		switch sv := v.(type) {
		case nil:
		case string:
			for _, r := range sv {
				if r < 0x20 || r == 0x7f { // CR/LF/控制字符：非法头值
					return fmt.Errorf("header_override value for %q contains control characters", k)
				}
			}
			if authHeaderKeys[canon] && !strings.Contains(sv, credentialPlaceholder) {
				return fmt.Errorf("header_override auth header %q must use the %s placeholder — put the real key in the channel api_key field (envelope-encrypted), never plaintext here", k, credentialPlaceholder)
			}
		default:
			return fmt.Errorf("header_override value for %q must be a string or null", k)
		}
	}
	return nil
}

// OverrideKeys：审计用——只暴露 keys 列表（排序稳定），绝不暴露值。解析失败返回 nil。
func OverrideKeys(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var m map[string]any
	if json.Unmarshal([]byte(s), &m) != nil {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// applyParamOverride：返回应用 override 后的请求体。无 override（或解析失败——写入时已校验，
// 此处防御）时原样返回**同一个 map**（零拷贝零开销）；有 override 时返回副本，原 map 不动。
func applyParamOverride(req map[string]any, overrideJSON string) map[string]any {
	if strings.TrimSpace(overrideJSON) == "" {
		return req
	}
	var override map[string]any
	if json.Unmarshal([]byte(overrideJSON), &override) != nil || len(override) == 0 {
		return req
	}
	merged := maps.Clone(req)
	for k, v := range override {
		if protectedParamKeys[k] {
			continue // 防御性跳过（写入侧已拒绝；直改 DB 也绕不过资金钳制）
		}
		if v == nil {
			delete(merged, k)
			continue
		}
		if ov, ok := v.(map[string]any); ok {
			if base, ok2 := merged[k].(map[string]any); ok2 {
				merged[k] = mergeOverrideMap(base, ov)
				continue
			}
			// 基底不是对象（缺失/标量）：null 值剥离后整体覆盖。剥空则不设——
			// delete-only 嵌套 override 在基底缺键时是 no-op，绝不物化 {} 注入请求体
			// （空对象会被部分上游 400，对抗 review）。
			if pruned := pruneNulls(ov); len(pruned) > 0 {
				merged[k] = pruned
			}
			continue
		}
		merged[k] = v
	}
	return merged
}

// mergeOverrideMap：递归合并（copy-on-write：克隆 base，绝不改动原 map）。
// override 值 null → 删除；双方皆对象 → 递归；否则覆盖。
func mergeOverrideMap(base, override map[string]any) map[string]any {
	out := maps.Clone(base)
	for k, v := range override {
		if v == nil {
			delete(out, k)
			continue
		}
		if ov, ok := v.(map[string]any); ok {
			if bv, ok2 := out[k].(map[string]any); ok2 {
				out[k] = mergeOverrideMap(bv, ov)
				continue
			}
			if pruned := pruneNulls(ov); len(pruned) > 0 { // 剥空不设（同顶层语义）
				out[k] = pruned
			}
			continue
		}
		out[k] = v
	}
	return out
}

// pruneNulls：整体覆盖场景下剥离 override 对象里的 null 项（null 只有"删除既有字段"语义）。
func pruneNulls(m map[string]any) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		if v == nil {
			continue
		}
		if mv, ok := v.(map[string]any); ok {
			out[k] = pruneNulls(mv)
			continue
		}
		out[k] = v
	}
	return out
}

// ensureIncludeUsage：流式请求强制 stream_options.include_usage=true（计费依赖末帧 usage，
// 绝不许被客户端/override 关掉），同时**保留**既有兄弟字段（对抗 review：此前三个 openai 系
// adapter 整体替换 stream_options，运营 override 增设的字段被静默丢弃）。
// clone 而非原地改：无 override 时 req 的嵌套 map 与原请求共享，原地改会跨 attempt 写穿。
func ensureIncludeUsage(req map[string]any) {
	so, _ := req["stream_options"].(map[string]any)
	merged := make(map[string]any, len(so)+1)
	for k, v := range so {
		merged[k] = v
	}
	merged["include_usage"] = true
	req["stream_options"] = merged
}

// applyHeaderOverride：adapter 默认头之后应用（运营配置优先级最高）。
// 值 null → 删头；string → 设置（{{credential}} 占位符注入解密凭证）。禁改头防御性跳过。
func applyHeaderOverride(upReq *http.Request, overrideJSON, credential string) {
	if strings.TrimSpace(overrideJSON) == "" {
		return
	}
	var override map[string]any
	if json.Unmarshal([]byte(overrideJSON), &override) != nil {
		return
	}
	for k, v := range override {
		if forbiddenHeaderKeys[http.CanonicalHeaderKey(k)] {
			continue
		}
		switch sv := v.(type) {
		case nil:
			upReq.Header.Del(k)
		case string:
			upReq.Header.Set(k, strings.ReplaceAll(sv, credentialPlaceholder, credential))
		}
	}
}
