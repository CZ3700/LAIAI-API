package forward

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"
)

// CategoryEndpoint 返回模型类别对应的标准 OpenAI 兼容入口路径。
func CategoryEndpoint(category string) string {
	switch strings.ToLower(strings.TrimSpace(category)) {
	case "图片", "image", "images":
		return "/v1/images/generations"
	case "视频", "video", "videos":
		return "/v1/videos"
	case "音频", "audio", "speech", "tts":
		return "/v1/audio/speech"
	case "嵌入", "embedding", "embeddings":
		return "/v1/embeddings"
	default:
		return "/v1/chat/completions"
	}
}

func pathEq(a, b string) bool {
	return strings.TrimLeft(strings.TrimSpace(a), "/") == strings.TrimLeft(strings.TrimSpace(b), "/")
}

// IsAnthropicMessagesPath 判断入口是否为 Anthropic Messages 原生/兼容路由。
func IsAnthropicMessagesPath(path string) bool {
	return pathEq(path, "/v1/messages") ||
		pathEq(path, "/anthropic/v1/messages") ||
		pathEq(path, "/api/compatible/v1/messages")
}

// PathMatches 比较请求路径与候选模板（忽略首部 '/'，支持末尾后缀与 ${model} 占位）。
func PathMatches(reqPath, candidate string) bool {
	req := strings.TrimLeft(strings.TrimSpace(reqPath), "/")
	cand := strings.TrimLeft(strings.TrimSpace(candidate), "/")
	if cand == "" {
		return false
	}
	if req == cand || strings.HasSuffix(req, cand) {
		return true
	}
	if prefix, suffix, ok := strings.Cut(cand, "${model}"); ok {
		return strings.HasPrefix(req, prefix) &&
			strings.HasSuffix(req, suffix) &&
			len(req) >= len(prefix)+len(suffix)
	}
	return false
}

// CollectRewrites 提取 config 中的路径重写表（优先 path_rewrites，兼容 path_rewrite）。
func CollectRewrites(cfg ForwardConfig) []PathRewritePair {
	out := make([]PathRewritePair, 0, len(cfg.PathRewrites)+1)
	for _, p := range cfg.PathRewrites {
		if p.Old != "" || p.New != "" {
			out = append(out, p)
		}
	}
	if len(out) == 0 && cfg.PathRewrite != nil && (cfg.PathRewrite.Old != "" || cfg.PathRewrite.New != "") {
		out = append(out, *cfg.PathRewrite)
	}
	return out
}

func pathListHit(list []string, reqPath string) bool {
	for _, p := range list {
		if p != "" && pathEq(reqPath, p) {
			return true
		}
	}
	return false
}

// ExpandUpstreamPath 展开 ${model}/{model} 与 ${task_id}/{task_id} 模板变量。
func ExpandUpstreamPath(tpl, model, taskID string) string {
	out := tpl
	if model != "" {
		out = strings.ReplaceAll(out, "${model}", model)
		out = strings.ReplaceAll(out, "{model}", model)
	}
	if taskID != "" {
		out = strings.ReplaceAll(out, "${task_id}", taskID)
		out = strings.ReplaceAll(out, "{task_id}", taskID)
	}
	return out
}

// ParseForwardConfig 解析 forward_rules.config_json 生成 ResolvedForward。
func ParseForwardConfig(
	configJSON string,
	categoryPath string,
	requestPath string,
	eid string,
	ruleID uint64,
	ruleName string,
	model string,
) (ResolvedForward, error) {
	trimmed := strings.TrimSpace(configJSON)
	if trimmed == "" {
		trimmed = "{}"
	}
	var cfg ForwardConfig
	if err := json.Unmarshal([]byte(trimmed), &cfg); err != nil {
		return ResolvedForward{}, fmt.Errorf("config_json 解析失败: %w", err)
	}
	var raw map[string]json.RawMessage
	_ = json.Unmarshal([]byte(trimmed), &raw)

	isPassthrough := pathListHit(cfg.PathPassthrough, requestPath)
	defaultTT, defaultAuth := "openai", "bearer"
	if isPassthrough && IsAnthropicMessagesPath(requestPath) {
		defaultTT, defaultAuth = "anthropic", "x-api-key"
	}

	upstreamPath := categoryPath
	if isPassthrough {
		upstreamPath = requestPath
	} else {
		rewrites := CollectRewrites(cfg)
		matched := false
		for _, p := range rewrites {
			if PathMatches(requestPath, p.New) || PathMatches(requestPath, p.Old) {
				upstreamPath = p.New
				matched = true
				break
			}
		}
		if !matched {
			for _, p := range rewrites {
				if p.Old != "" && strings.Contains(categoryPath, p.Old) {
					upstreamPath = strings.Replace(categoryPath, p.Old, p.New, 1)
					matched = true
					break
				}
			}
		}
		if !matched {
			for _, p := range rewrites {
				if p.New != "" {
					upstreamPath = p.New
					break
				}
			}
		}
	}

	tt := strings.TrimSpace(cfg.TargetType)
	if tt == "" {
		tt = defaultTT
	}
	authType := strings.TrimSpace(cfg.AuthType)
	if authType == "" {
		authType = defaultAuth
	}
	ns := strings.TrimSpace(cfg.AssetConvertNS)
	if ns == "" {
		ns = "asset_manager"
	}

	return ResolvedForward{
		RuleID:                 ruleID,
		EID:                    eid,
		RuleName:               ruleName,
		Model:                  model,
		TargetType:             tt,
		UpstreamPath:           ExpandUpstreamPath(upstreamPath, model, ""),
		AuthType:               authType,
		PollPath:               strings.TrimSpace(cfg.PollPath),
		IsPassthrough:          isPassthrough,
		AssetConvert:           cfg.AssetConvert,
		AssetConvertNS:         ns,
		UpstreamAssetConvert:   cfg.UpstreamAssetConvert,
		UpstreamAssetBindingID: cfg.UpstreamAssetBindingID,
		AssetModeration:        cfg.Moderation,
		ContentToPrompt:        cfg.ContentToPrompt,
		FastResponse:           cfg.FastResponse,
		AllowedResolutions:     cfg.AllowedResolutions,
		AllowedQualities:       cfg.AllowedQualities,
		MaxRefImages:           cfg.MaxRefImages,
		MinPixels:              cfg.MinPixels,
		MaxPixels:              cfg.MaxPixels,
		AutoDowngrade:          cfg.AutoDowngrade,
		Step:                   cfg.Step,
		MaxAspectRatio:         cfg.MaxAspectRatio,
		Raw:                    raw,
	}, nil
}

// ValidateConfigJSON 校验转发规则 config_json 语法是否有效且包含合法字段。
func ValidateConfigJSON(configJSON string) error {
	trimmed := strings.TrimSpace(configJSON)
	if trimmed == "" || trimmed == "{}" {
		return nil
	}
	var cfg ForwardConfig
	if err := json.Unmarshal([]byte(trimmed), &cfg); err != nil {
		return fmt.Errorf("config_json 格式无效: %w", err)
	}
	return nil
}

// SelectBestForwardRule 按精确度 3/2/1 评分选出最匹配请求路径的规则：
//
//	3 分 — path_passthrough / path_accept 命中，或非 OpenAI 路径匹配 path_rewrite.old/new
//	2 分 — OpenAI 基准请求且规则为纯透传（old == new）
//	1 分 — OpenAI 基准请求且规则为协议转换型（old != new）
func SelectBestForwardRule(rules []ForwardRule, category, requestPath string) (*ForwardRule, int) {
	if len(rules) == 0 {
		return nil, 0
	}
	cat := strings.TrimSpace(category)
	var candidates []ForwardRule
	if cat != "" {
		for _, r := range rules {
			if r.IsActive == 1 && (r.Category == cat || r.Category == "封装") {
				candidates = append(candidates, r)
			}
		}
	}
	if len(candidates) == 0 {
		for _, r := range rules {
			if r.IsActive == 1 {
				candidates = append(candidates, r)
			}
		}
	}

	openaiPath := CategoryEndpoint(cat)
	openaiClean := strings.TrimLeft(openaiPath, "/")
	reqClean := strings.TrimLeft(strings.TrimSpace(requestPath), "/")
	isOpenAIReq := reqClean == openaiClean || strings.HasSuffix(reqClean, openaiClean)

	var best *ForwardRule
	bestScore := 0
	for i := range candidates {
		r := &candidates[i]
		trimmed := strings.TrimSpace(r.ConfigJSON)
		if trimmed == "" {
			trimmed = "{}"
		}
		var cfg ForwardConfig
		if json.Unmarshal([]byte(trimmed), &cfg) != nil {
			continue
		}
		score := 0
		if pathListHit(cfg.PathPassthrough, requestPath) || pathListHit(cfg.PathAccept, requestPath) {
			score = 3
		} else {
			rewrites := CollectRewrites(cfg)
			for _, p := range rewrites {
				oldClean := strings.TrimLeft(strings.TrimSpace(p.Old), "/")
				newClean := strings.TrimLeft(strings.TrimSpace(p.New), "/")
				supportsOpenAI := oldClean == "" || oldClean == openaiClean || strings.HasSuffix(openaiClean, oldClean)
				matchNew := PathMatches(requestPath, p.New)
				matchOld := PathMatches(requestPath, p.Old)

				s := 0
				switch {
				case !isOpenAIReq && (matchNew || matchOld):
					s = 3
				case isOpenAIReq && supportsOpenAI && oldClean == newClean:
					s = 2
				case isOpenAIReq && supportsOpenAI:
					s = 1
				}
				if s > score {
					score = s
					if score == 3 {
						break
					}
				}
			}
		}
		if score > bestScore {
			bestScore = score
			best = r
			if bestScore == 3 {
				break
			}
		}
	}
	return best, bestScore
}

// Resolver 提供带本地 TTL 缓存的模型转发规则解析。
type Resolver struct {
	ttl time.Duration
	mu  sync.RWMutex

	bindingsLoadedAt time.Time
	bindingsByModel  map[string]bindingRow // key: model

	rulesLoadedAt time.Time
	rulesByID     map[uint64]ForwardRule
}

type bindingRow struct {
	Model          string `gorm:"column:model"`
	ForwardRuleIDs string `gorm:"column:forward_rule_ids"`
	IsActive       int8   `gorm:"column:is_active"`
}

func NewResolver(ttl time.Duration) *Resolver {
	if ttl <= 0 {
		ttl = 15 * time.Second
	}
	return &Resolver{
		ttl:             ttl,
		bindingsByModel: make(map[string]bindingRow),
		rulesByID:       make(map[uint64]ForwardRule),
	}
}

// Invalidate 清空内存缓存（在后台 CRUD 或 Redis 广播到达时调用）。
func (r *Resolver) Invalidate() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.bindingsLoadedAt = time.Time{}
	r.rulesLoadedAt = time.Time{}
}

func (r *Resolver) ensureLoaded(ctx context.Context, db *gorm.DB) error {
	now := time.Now()
	r.mu.RLock()
	fresh := !r.bindingsLoadedAt.IsZero() && now.Sub(r.bindingsLoadedAt) < r.ttl &&
		!r.rulesLoadedAt.IsZero() && now.Sub(r.rulesLoadedAt) < r.ttl
	r.mu.RUnlock()
	if fresh {
		return nil
	}

	var bindings []bindingRow
	if err := db.WithContext(ctx).
		Table("model_rule_bindings").
		Select("model, forward_rule_ids, is_active").
		Where("is_active = 1").
		Find(&bindings).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}

	var rules []ForwardRule
	if err := db.WithContext(ctx).
		Where("is_active = 1").
		Order("sort_order ASC, id ASC").
		Find(&rules).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}

	bMap := make(map[string]bindingRow, len(bindings))
	for _, b := range bindings {
		bMap[b.Model] = b
	}
	rMap := make(map[uint64]ForwardRule, len(rules))
	for _, fr := range rules {
		rMap[fr.ID] = fr
	}

	r.mu.Lock()
	r.bindingsByModel = bMap
	r.bindingsLoadedAt = now
	r.rulesByID = rMap
	r.rulesLoadedAt = now
	r.mu.Unlock()
	return nil
}

// ResolveForModel 根据模型名、类别与入口请求路径解析命中的转发规则；无绑定或无匹配时返回 (nil, nil)。
func (r *Resolver) ResolveForModel(
	ctx context.Context,
	db *gorm.DB,
	model string,
	category string,
	requestPath string,
) (*ResolvedForward, error) {
	if db == nil || strings.TrimSpace(model) == "" {
		return nil, nil
	}
	if err := r.ensureLoaded(ctx, db); err != nil {
		return nil, err
	}

	r.mu.RLock()
	b, ok := r.bindingsByModel[model]
	if !ok || b.IsActive != 1 {
		r.mu.RUnlock()
		return nil, nil
	}
	var ids []uint64
	_ = json.Unmarshal([]byte(b.ForwardRuleIDs), &ids)
	if len(ids) == 0 {
		r.mu.RUnlock()
		return nil, nil
	}
	boundRules := make([]ForwardRule, 0, len(ids))
	for _, id := range ids {
		if fr, exists := r.rulesByID[id]; exists && fr.IsActive == 1 {
			boundRules = append(boundRules, fr)
		}
	}
	r.mu.RUnlock()

	best, score := SelectBestForwardRule(boundRules, category, requestPath)
	if best == nil || score == 0 {
		return nil, nil
	}
	resolved, err := ParseForwardConfig(
		best.ConfigJSON,
		CategoryEndpoint(category),
		requestPath,
		best.EID,
		best.ID,
		best.Name,
		model,
	)
	if err != nil {
		return nil, err
	}
	return &resolved, nil
}
