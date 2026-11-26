package forward

import (
	"cmp"
	"math"
	"strconv"
	"strings"
)

// IsBase64Media 判断输入是否为 data URI 或长度达标的纯 Base64 载荷（非 http 链接）。
func IsBase64Media(s string, minLen int) bool {
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return false
	}
	if strings.HasPrefix(trimmed, "data:") {
		return true
	}
	return !strings.HasPrefix(trimmed, "http") && len(trimmed) >= minLen
}

// StripDataURIPrefix 去除 data:image/...;base64, 前缀，返回纯 Base64 字符串。
func StripDataURIPrefix(s string) string {
	trimmed := strings.TrimSpace(s)
	if strings.HasPrefix(trimmed, "data:") {
		if _, data, ok := strings.Cut(trimmed, ","); ok {
			return data
		}
	}
	return trimmed
}

// JSONTruthy 兼容布尔 true、数值 1 及字符串 "true"/"1"/"enabled"。
func JSONTruthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case float64:
		return x != 0
	case int:
		return x != 0
	case int64:
		return x != 0
	case string:
		t := strings.ToLower(strings.TrimSpace(x))
		return t == "true" || t == "1" || t == "enabled" || t == "yes"
	default:
		return false
	}
}

// AsMediaURL 从字符串或 {"url": "..."} 对象提取媒体 URL。
func AsMediaURL(v any) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case map[string]any:
		if s, ok := x["url"].(string); ok {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// CollectMediaValues 按字段优先级收集媒体元素数组（保留 role/type 元数据）。
func CollectMediaValues(body map[string]any, fields []string) []any {
	if len(body) == 0 {
		return nil
	}
	for _, field := range fields {
		val, exists := body[field]
		if !exists || val == nil {
			continue
		}
		out := make([]any, 0, 2)
		switch x := val.(type) {
		case string:
			if strings.TrimSpace(x) != "" {
				out = append(out, x)
			}
		case []any:
			for _, item := range x {
				switch it := item.(type) {
				case string:
					if strings.TrimSpace(it) != "" {
						out = append(out, it)
					}
				case map[string]any:
					out = append(out, it)
				}
			}
		case []string:
			for _, s := range x {
				if strings.TrimSpace(s) != "" {
					out = append(out, s)
				}
			}
		case []map[string]any:
			for _, m := range x {
				out = append(out, m)
			}
		case map[string]any:
			out = append(out, x)
		}
		if len(out) > 0 {
			return out
		}
	}
	return nil
}

// CollectURLs 按字段优先级提取纯 URL 字符串切片。
func CollectURLs(body map[string]any, fields []string) []string {
	elements := CollectMediaValues(body, fields)
	if len(elements) == 0 {
		return nil
	}
	urls := make([]string, 0, len(elements))
	for _, item := range elements {
		if u := AsMediaURL(item); u != "" {
			urls = append(urls, u)
			continue
		}
		if m, ok := item.(map[string]any); ok {
			for _, key := range []string{"image", "image_url", "video_url", "audio_url", "url"} {
				if u := AsMediaURL(m[key]); u != "" {
					urls = append(urls, u)
					break
				}
			}
		}
	}
	return urls
}

// ParseMediaItem 提取单个媒体项的 (url, role)。
func ParseMediaItem(item any, defaultRole string) (string, string) {
	switch x := item.(type) {
	case string:
		trimmed := strings.TrimSpace(x)
		if trimmed != "" {
			return trimmed, defaultRole
		}
	case map[string]any:
		var foundURL string
		for _, key := range []string{"image", "image_url", "video_url", "audio_url", "url"} {
			if u := AsMediaURL(x[key]); u != "" {
				foundURL = u
				break
			}
		}
		role, _ := x["role"].(string)
		if role == "" {
			role, _ = x["type"].(string)
		}
		return foundURL, cmp.Or(strings.TrimSpace(role), defaultRole)
	}
	return "", defaultRole
}

// InferImageDefaultRoles 按图片张数推断默认角色：1->first_frame，2->first_frame+last_frame，3+->reference_image。
func InferImageDefaultRoles(count int) []string {
	switch count {
	case 0:
		return nil
	case 1:
		return []string{"first_frame"}
	case 2:
		return []string{"first_frame", "last_frame"}
	default:
		roles := make([]string, count)
		for i := range count {
			roles[i] = "reference_image"
		}
		return roles
	}
}

// EnsureImageRolesForMultimodal 在 content 同时含 video_url 或 audio_url 时将全部 image_url.role 统一修正为 reference_image。
func EnsureImageRolesForMultimodal(content []any) []any {
	hasRefMedia := false
	for _, raw := range content {
		m, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		ty, _ := m["type"].(string)
		if ty == "video_url" || ty == "audio_url" {
			hasRefMedia = true
			break
		}
	}
	if !hasRefMedia {
		return content
	}
	out := make([]any, len(content))
	for i, raw := range content {
		m, ok := raw.(map[string]any)
		if !ok {
			out[i] = raw
			continue
		}
		ty, _ := m["type"].(string)
		if ty != "image_url" {
			out[i] = m
			continue
		}
		cp := make(map[string]any, len(m)+1)
		for k, v := range m {
			cp[k] = v
		}
		cp["role"] = "reference_image"
		out[i] = cp
	}
	return out
}

// SizeToRatio 将像素尺寸（如 1280x720）或显式比例（16:9）归一化为最近标准宽高比。
func SizeToRatio(size string) string {
	s := strings.TrimSpace(size)
	if s == "" {
		return ""
	}
	if strings.Contains(s, ":") || strings.EqualFold(s, "auto") || strings.EqualFold(s, "adaptive") {
		return s
	}
	var wStr, hStr string
	var ok bool
	for _, sep := range []string{"x", "X", "*", "×"} {
		if wStr, hStr, ok = strings.Cut(s, sep); ok {
			break
		}
	}
	if !ok {
		return ""
	}
	w, err1 := strconv.ParseFloat(strings.TrimSpace(wStr), 64)
	h, err2 := strconv.ParseFloat(strings.TrimSpace(hStr), 64)
	if err1 != nil || err2 != nil || w <= 0 || h <= 0 {
		return ""
	}
	ratio := w / h
	candidates := []struct {
		label string
		val   float64
	}{
		{"1:1", 1.0},
		{"3:2", 1.5},
		{"4:3", 4.0 / 3.0},
		{"16:9", 16.0 / 9.0},
		{"21:9", 21.0 / 9.0},
		{"2:3", 2.0 / 3.0},
		{"3:4", 0.75},
		{"9:16", 9.0 / 16.0},
	}
	bestLabel := candidates[0].label
	bestDiff := math.Abs(ratio - candidates[0].val)
	for _, c := range candidates[1:] {
		if diff := math.Abs(ratio - c.val); diff < bestDiff {
			bestDiff = diff
			bestLabel = c.label
		}
	}
	return bestLabel
}
