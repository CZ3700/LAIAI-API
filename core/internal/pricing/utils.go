package pricing

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/shopspring/decimal"
)

func splitPromptSubsets(prompt, cached, cacheWrite int64, billCached, billWrite, isOpenAI bool) (int64, int64, int64) {
	if cached < 0 {
		cached = 0
	}
	if cacheWrite < 0 {
		cacheWrite = 0
	}
	var billC, billW int64
	if billCached {
		billC = cached
	}
	if billWrite {
		billW = cacheWrite
	}
	if isOpenAI && prompt >= cached+cacheWrite {
		return prompt - billC - billW, billC, billW
	}
	return prompt, billC, billW
}

func parseExtMap(extJSON string) map[string]json.RawMessage {
	if strings.TrimSpace(extJSON) == "" || extJSON == "{}" {
		return nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal([]byte(extJSON), &m); err != nil {
		return nil
	}
	return m
}

func extDecimal(ext map[string]json.RawMessage, key string) (decimal.Decimal, bool) {
	if ext == nil {
		return decimal.Zero, false
	}
	raw, ok := ext[key]
	if !ok {
		return decimal.Zero, false
	}
	var num json.Number
	if err := json.Unmarshal(raw, &num); err == nil {
		if d, err := decimal.NewFromString(num.String()); err == nil {
			return d, true
		}
	}
	var str string
	if err := json.Unmarshal(raw, &str); err == nil {
		if d, err := decimal.NewFromString(strings.TrimSpace(str)); err == nil {
			return d, true
		}
	}
	return decimal.Zero, false
}

func extBool(ext map[string]json.RawMessage, key string, def bool) bool {
	if ext == nil {
		return def
	}
	raw, ok := ext[key]
	if !ok {
		return def
	}
	var b bool
	if err := json.Unmarshal(raw, &b); err == nil {
		return b
	}
	return def
}

func freeImageCountFromExt(extJSON string, def int64) int64 {
	ext := parseExtMap(extJSON)
	if d, ok := extDecimal(ext, "free_image_count"); ok {
		v := d.IntPart()
		if v < 0 {
			return 0
		}
		return v
	}
	return def
}

// lookupPriceTable 从 extended_config.price_table 精确或大小写不敏感查价。
func lookupPriceTable(ext map[string]json.RawMessage, tableKey string) (decimal.Decimal, bool) {
	if ext == nil {
		return decimal.Zero, false
	}
	if rawDisabled, ok := ext["price_table_disabled"]; ok {
		var disabled []string
		if json.Unmarshal(rawDisabled, &disabled) == nil {
			lk := strings.ToLower(tableKey)
			for _, k := range disabled {
				if strings.ToLower(k) == lk {
					return decimal.Zero, false
				}
			}
		}
	}
	rawPT, ok := ext["price_table"]
	if !ok {
		return decimal.Zero, false
	}
	var pt map[string]json.RawMessage
	if err := json.Unmarshal(rawPT, &pt); err != nil {
		return decimal.Zero, false
	}
	if raw, ok := pt[tableKey]; ok {
		if d, ok := parseJSONDecimal(raw); ok {
			return d, true
		}
	}
	lk := strings.ToLower(tableKey)
	for k, raw := range pt {
		if strings.ToLower(k) == lk {
			if d, ok := parseJSONDecimal(raw); ok {
				return d, true
			}
		}
	}
	return decimal.Zero, false
}

func lookupPriceTablePrefixMax(ext map[string]json.RawMessage, prefix string) (string, decimal.Decimal, bool) {
	if ext == nil {
		return "", decimal.Zero, false
	}
	rawPT, ok := ext["price_table"]
	if !ok {
		return "", decimal.Zero, false
	}
	var pt map[string]json.RawMessage
	if err := json.Unmarshal(rawPT, &pt); err != nil {
		return "", decimal.Zero, false
	}
	prefixLower := strings.ToLower(prefix) + "|"
	var bestKey string
	var bestVal decimal.Decimal
	found := false
	for k, raw := range pt {
		if !strings.HasPrefix(strings.ToLower(k), prefixLower) {
			continue
		}
		if d, ok := parseJSONDecimal(raw); ok {
			if !found || d.GreaterThan(bestVal) {
				bestKey = k
				bestVal = d
				found = true
			}
		}
	}
	return bestKey, bestVal, found
}

func parseJSONDecimal(raw json.RawMessage) (decimal.Decimal, bool) {
	var num json.Number
	if err := json.Unmarshal(raw, &num); err == nil {
		if d, err := decimal.NewFromString(num.String()); err == nil {
			return d, true
		}
	}
	var str string
	if err := json.Unmarshal(raw, &str); err == nil {
		if d, err := decimal.NewFromString(strings.TrimSpace(str)); err == nil {
			return d, true
		}
	}
	return decimal.Zero, false
}

func translateBillingKey(key string) string {
	parts := strings.Split(key, "|")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		switch strings.ToLower(strings.TrimSpace(p)) {
		case "std", "standard":
			out = append(out, "标准")
		case "pro":
			out = append(out, "专业")
		case "fast":
			out = append(out, "极速")
		case "ai":
			out = append(out, "大模型")
		case "text":
			out = append(out, "文生")
		case "image":
			out = append(out, "图生")
		case "ref":
			out = append(out, "视频参考")
		case "on":
			out = append(out, "有声")
		case "off":
			out = append(out, "无声")
		case "yes":
			out = append(out, "含视频")
		case "no":
			out = append(out, "无视频")
		case "img2img":
			out = append(out, "图生图")
		case "ref_1_3":
			out = append(out, "1-3张参考")
		case "ref_4_7":
			out = append(out, "4-7张参考")
		default:
			out = append(out, strings.ToLower(strings.TrimSpace(p)))
		}
	}
	return strings.Join(out, "|")
}

// NormalizePixelSize 将 size 统一规范为 "WxH"（支持 x/*/\u00d7/: 与 1k/2k/4k）。
func NormalizePixelSize(size string) string {
	s := strings.ToLower(strings.TrimSpace(size))
	if strings.HasSuffix(s, "k") {
		numStr := strings.TrimSuffix(s, "k")
		if d, err := decimal.NewFromString(numStr); err == nil && d.IsPositive() {
			px := d.Mul(decimal.NewFromInt(1024)).Round(0).IntPart()
			return fmt.Sprintf("%dx%d", px, px)
		}
	}
	r := strings.NewReplacer("*", "x", "\u00d7", "x", ":", "x")
	norm := r.Replace(s)
	parts := strings.Split(norm, "x")
	if len(parts) == 2 {
		w, e1 := strconv.ParseUint(strings.TrimSpace(parts[0]), 10, 32)
		h, e2 := strconv.ParseUint(strings.TrimSpace(parts[1]), 10, 32)
		if e1 == nil && e2 == nil {
			return fmt.Sprintf("%dx%d", w, h)
		}
	}
	return ""
}

// PixelsWanFromSize 返回输出总像素（万）；解析失败按 1024x1024 = 104.8576 万。
func PixelsWanFromSize(size string) decimal.Decimal {
	norm := NormalizePixelSize(size)
	if wStr, hStr, ok := strings.Cut(norm, "x"); ok {
		w, e1 := decimal.NewFromString(wStr)
		h, e2 := decimal.NewFromString(hStr)
		if e1 == nil && e2 == nil && w.IsPositive() && h.IsPositive() {
			return w.Mul(h).Div(tenThousand)
		}
	}
	return decimal.RequireFromString("104.8576")
}

// NormalizeResolutionLabel 规范化分辨率标签（"720P"->"720p", "1080"->"1080p" 等）。
func NormalizeResolutionLabel(raw string) string {
	s := strings.ToLower(strings.TrimSpace(raw))
	switch s {
	case "":
		return ""
	case "480", "480p":
		return "480p"
	case "540", "540p":
		return "540p"
	case "720", "720p":
		return "720p"
	case "768", "768p":
		return "768p"
	case "1080", "1080p":
		return "1080p"
	case "2k", "2kp":
		return "2k"
	case "4k", "4kp":
		return "4k"
	default:
		return s
	}
}
