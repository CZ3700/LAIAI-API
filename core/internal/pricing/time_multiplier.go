package pricing

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/shopspring/decimal"
)

// TimeMultiplier 峰谷分时倍率窗口（半开区间 [Start, End)，支持跨天与星期过滤）。
type TimeMultiplier struct {
	Start      string          `json:"start"`
	End        string          `json:"end"`
	Multiplier decimal.Decimal `json:"multiplier"`
	Days       []int           `json:"days"`    // ISO 星期：1=周一 .. 7=周日；空=每天
	AllDay     bool            `json:"all_day"` // 当天全天生效
}

// TimeMultiplierConfig extended_config 中的时段倍率配置。
type TimeMultiplierConfig struct {
	EnableTimeMultipliers     bool             `json:"enable_time_multipliers"`
	InvertTimeMultipliers     bool             `json:"invert_time_multipliers"`
	InvertRemainderMultiplier *decimal.Decimal `json:"invert_remainder_multiplier,omitempty"`
	TimeMultipliers           []TimeMultiplier `json:"time_multipliers"`
}

func isoWeekday(wd time.Weekday) int {
	if wd == time.Sunday {
		return 7
	}
	return int(wd)
}

func prevWeekday(wd time.Weekday) time.Weekday {
	if wd == time.Sunday {
		return time.Saturday
	}
	return wd - 1
}

func weekdayApplies(days []int, wd time.Weekday) bool {
	if len(days) == 0 {
		return true
	}
	n := isoWeekday(wd)
	for _, d := range days {
		if d == n {
			return true
		}
	}
	return false
}

func parseHHMM(s string) (int, bool) {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) != 2 {
		return 0, false
	}
	var h, m int
	if _, err := fmt.Sscanf(parts[0], "%d", &h); err != nil || h < 0 || h > 23 {
		return 0, false
	}
	if _, err := fmt.Sscanf(parts[1], "%d", &m); err != nil || m < 0 || m > 59 {
		return 0, false
	}
	return h*60 + m, true
}

func intervalMatches(item TimeMultiplier, curMin int, wd time.Weekday) bool {
	if item.AllDay {
		return false
	}
	startMin, ok1 := parseHHMM(item.Start)
	endMin, ok2 := parseHHMM(item.End)
	if !ok1 || !ok2 {
		return false
	}
	if startMin > endMin {
		// 跨天窗口归属起始日
		if curMin >= startMin {
			return weekdayApplies(item.Days, wd)
		}
		if curMin < endMin {
			return weekdayApplies(item.Days, prevWeekday(wd))
		}
		return false
	}
	return curMin >= startMin && curMin < endMin && weekdayApplies(item.Days, wd)
}

func dayHasAllDay(list []TimeMultiplier, wd time.Weekday) bool {
	for _, item := range list {
		if item.AllDay && weekdayApplies(item.Days, wd) {
			return true
		}
	}
	return false
}

func invertRestRate(list []TimeMultiplier, wd time.Weekday, rem *decimal.Decimal) decimal.Decimal {
	if rem != nil && !rem.IsNegative() {
		return *rem
	}
	for _, item := range list {
		if !item.AllDay && weekdayApplies(item.Days, wd) {
			if item.Multiplier.IsPositive() {
				return decimal.NewFromInt(1).Div(item.Multiplier)
			}
			return decimal.NewFromInt(1)
		}
	}
	return decimal.NewFromInt(1)
}

// ResolveTimeMultiplierAt 按本地时间解析时段倍率（半开区间 [start, end)，支持跨天与反向倍率）。
func ResolveTimeMultiplierAt(rule *BillingRule, now time.Time, loc *time.Location) decimal.Decimal {
	one := decimal.NewFromInt(1)
	if rule == nil || strings.TrimSpace(rule.ExtendedConfig) == "" || rule.ExtendedConfig == "{}" {
		return one
	}
	var cfg TimeMultiplierConfig
	if err := json.Unmarshal([]byte(rule.ExtendedConfig), &cfg); err != nil || !cfg.EnableTimeMultipliers {
		return one
	}
	if len(cfg.TimeMultipliers) == 0 {
		return one
	}
	if loc == nil {
		loc = time.FixedZone("CST", 8*3600)
	}
	local := now.In(loc)
	curMin := local.Hour()*60 + local.Minute()
	wd := local.Weekday()

	hasValidRem := cfg.InvertRemainderMultiplier != nil && !cfg.InvertRemainderMultiplier.IsNegative()
	for _, item := range cfg.TimeMultipliers {
		if intervalMatches(item, curMin, wd) {
			if cfg.InvertTimeMultipliers && !hasValidRem && !dayHasAllDay(cfg.TimeMultipliers, wd) {
				return one
			}
			if !item.Multiplier.IsNegative() {
				return item.Multiplier
			}
			return one
		}
	}
	for _, item := range cfg.TimeMultipliers {
		if item.AllDay && weekdayApplies(item.Days, wd) {
			if !item.Multiplier.IsNegative() {
				return item.Multiplier
			}
			return one
		}
	}
	if hasValidRem || cfg.InvertTimeMultipliers {
		return invertRestRate(cfg.TimeMultipliers, wd, cfg.InvertRemainderMultiplier)
	}
	return one
}

// ValidateTimeMultiplierConfig 校验 extended_config 中时段倍率的合法性（创建/更新计费规则时调用）。
func ValidateTimeMultiplierConfig(extJSON string) error {
	trimmed := strings.TrimSpace(extJSON)
	if trimmed == "" || trimmed == "{}" {
		return nil
	}
	var cfg TimeMultiplierConfig
	if err := json.Unmarshal([]byte(trimmed), &cfg); err != nil {
		return fmt.Errorf("extended_config JSON 无效: %w", err)
	}
	if cfg.InvertRemainderMultiplier != nil && cfg.InvertRemainderMultiplier.IsNegative() {
		return fmt.Errorf("未规划时段倍率不能为负数")
	}
	for i, item := range cfg.TimeMultipliers {
		if item.Multiplier.IsNegative() {
			return fmt.Errorf("第 %d 条时段倍率不能为负数", i+1)
		}
		for _, d := range item.Days {
			if d < 1 || d > 7 {
				return fmt.Errorf("第 %d 条时段星期必须在 1..7 之间", i+1)
			}
		}
		if !item.AllDay {
			if _, ok := parseHHMM(item.Start); !ok {
				return fmt.Errorf("第 %d 条时段起始时间格式无效（需 HH:MM）", i+1)
			}
			if _, ok := parseHHMM(item.End); !ok {
				return fmt.Errorf("第 %d 条时段结束时间格式无效（需 HH:MM）", i+1)
			}
		}
	}
	return nil
}
