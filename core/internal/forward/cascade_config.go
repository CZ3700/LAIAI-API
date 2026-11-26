package forward

import (
	"cmp"
	"maps"
	"strings"
)

var defaultCascadeResMul = map[string]float64{
	"480p":  1.5,
	"720p":  2.15,
	"1080p": 2.25,
	"2k":    2.5,
	"4k":    4.0,
}

var defaultCascadeResBase = map[string]string{
	"480p":  "480p",
	"720p":  "480p",
	"1080p": "720p",
	"2k":    "1080p",
	"4k":    "1080p",
}

func cloneLowerFloatMap(in map[string]float64) map[string]float64 {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]float64, len(in))
	for k, v := range in {
		out[strings.ToLower(strings.TrimSpace(k))] = v
	}
	return out
}

func cloneLowerBoolMap(in map[string]FlexBool) map[string]bool {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]bool, len(in))
	for k, v := range in {
		out[strings.ToLower(strings.TrimSpace(k))] = bool(v)
	}
	return out
}

func cloneLowerStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
	}
	return out
}

func cloneLowerIntMap(in map[string]int) map[string]int {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]int, len(in))
	for k, v := range in {
		out[strings.ToLower(strings.TrimSpace(k))] = v
	}
	return out
}

// LookupResMul 返回级联超分规则在目标分辨率下的 token/用量倍率；非级联规则恒返回 1.0。
func (f *ResolvedForward) LookupResMul(targetRes string) float64 {
	if f == nil || !f.IsCascade {
		return 1.0
	}
	key := strings.ToLower(strings.TrimSpace(targetRes))
	if key == "" {
		key = "720p"
	}
	table := f.ResMul
	if len(table) == 0 {
		table = defaultCascadeResMul
	}
	if v, ok := table[key]; ok && v > 0 {
		return v
	}
	for k, v := range table {
		if strings.EqualFold(k, key) && v > 0 {
			return v
		}
	}
	if v, ok := defaultCascadeResMul[key]; ok && v > 0 {
		return v
	}
	return 1.5
}

// IsResEnhanceEnabled 判断该目标分辨率是否需要进入二阶段画质超分（480p 默认跳过 S2，720p+ 默认开启）。
func (f *ResolvedForward) IsResEnhanceEnabled(targetRes string) bool {
	if f == nil || !f.IsCascade {
		return false
	}
	key := strings.ToLower(strings.TrimSpace(targetRes))
	if key == "" {
		key = "720p"
	}
	if len(f.ResEnhanceEnabled) > 0 {
		if v, ok := f.ResEnhanceEnabled[key]; ok {
			return v
		}
		for k, v := range f.ResEnhanceEnabled {
			if strings.EqualFold(k, key) {
				return v
			}
		}
	}
	return key != "480p"
}

// CascadeResolveBase 解析一阶段提交给生成渠道的底稿分辨率（如 1080p -> 720p，720p -> 480p）。
func (f *ResolvedForward) CascadeResolveBase(targetRes string) string {
	key := strings.ToLower(strings.TrimSpace(targetRes))
	if key == "" {
		key = "720p"
	}
	if f != nil && len(f.ResBase) > 0 {
		if v, ok := f.ResBase[key]; ok && v != "" {
			return v
		}
		for k, v := range f.ResBase {
			if strings.EqualFold(k, key) && v != "" {
				return v
			}
		}
	}
	if v, ok := defaultCascadeResBase[key]; ok {
		return v
	}
	return "480p"
}

// ResolveVolcEnhanceTarget 解析二阶段火山 MediaKit 超分模式、上游路径与增强模型标识。
func (f *ResolvedForward) ResolveVolcEnhanceTarget(targetRes string) (mode, upstreamPath, model string) {
	key := strings.ToLower(strings.TrimSpace(targetRes))
	tier := "standard"
	if f != nil && len(f.ResEnhance) > 0 {
		if v, ok := f.ResEnhance[key]; ok && v != "" {
			tier = strings.ToLower(strings.TrimSpace(v))
		} else {
			for k, v := range f.ResEnhance {
				if strings.EqualFold(k, key) && v != "" {
					tier = strings.ToLower(strings.TrimSpace(v))
					break
				}
			}
		}
	}
	var overrideModel string
	if f != nil {
		overrideModel = strings.TrimSpace(f.CascadeS2Model)
	}
	switch tier {
	case "fast", "vve-ft":
		return "fast", "/api/v1/tools/enhance-video-fast", cmp.Or(overrideModel, "vve-ft")
	case "pro", "professional", "vve-pf":
		return "professional", "/api/v1/tools/enhance-video", cmp.Or(overrideModel, "vve-pf")
	case "ai", "generative", "vve-gt":
		return "generative", "/api/v1/tools/enhance-video-generative", cmp.Or(overrideModel, "vve-gt")
	default:
		return "standard", "/api/v1/tools/enhance-video", cmp.Or(overrideModel, "vve-sd")
	}
}

// DefaultCascadeResMulCopy 返回默认级联倍率表副本。
func DefaultCascadeResMulCopy() map[string]float64 {
	return maps.Clone(defaultCascadeResMul)
}
