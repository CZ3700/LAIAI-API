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
// 资金面：max_tokens/max_completion_tokens/n/thinking 参与 hold 估算钳制（relay.go P0-2 系列），
// override 若在钳制后抬高会让上游产出 > hold → overage 刷负余额。
// 路由面：model 由模型映射专管；stream 决定管道选择；messages 是用户内容不容运营改写。
var protectedParamKeys = map[string]bool{
	"model": true, "stream": true, "messages": true,
	"max_tokens": true, "max_completion_tokens": true, "n": true, "thinking": true,
}

// forbiddenHeaderKeys：header_override 禁改头（canonical 形态比较）。
var forbiddenHeaderKeys = map[string]bool{
	"Host": true, "Content-Length": true,
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

// ValidateHeaderOverride：admin 写入前校验。值必须是 string 或 null。
func ValidateHeaderOverride(s string) error {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		return fmt.Errorf("header_override must be a JSON object")
	}
	for k, v := range m {
		if forbiddenHeaderKeys[http.CanonicalHeaderKey(k)] {
			return fmt.Errorf("header_override key %q is forbidden (breaks HTTP semantics)", k)
		}
		switch v.(type) {
		case string, nil:
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
			// 基底不是对象（缺失/标量）：null 值剥离后整体覆盖
			merged[k] = pruneNulls(ov)
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
			out[k] = pruneNulls(ov)
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
