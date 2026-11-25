package forward

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
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
	return slices.ContainsFunc(list, func(p string) bool {
		return p != "" && pathEq(reqPath, p)
	})
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

func buildResolvedForward(
	cfg ForwardConfig,
	raw map[string]json.RawMessage,
	categoryPath string,
	requestPath string,
	eid string,
	ruleID uint64,
	ruleName string,
	model string,
) ResolvedForward {
	isPassthrough := pathListHit(cfg.PathPassthrough, requestPath)
	defaultTT, defaultAuth := "openai", AuthBearer
	if isPassthrough && IsAnthropicMessagesPath(requestPath) {
		defaultTT, defaultAuth = "anthropic", AuthXAPIKey
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

	return ResolvedForward{
		RuleID:                 ruleID,
		EID:                    eid,
		RuleName:               ruleName,
		Model:                  model,
		TargetType:             cmp.Or(strings.TrimSpace(cfg.TargetType), defaultTT),
		UpstreamPath:           ExpandUpstreamPath(upstreamPath, model, ""),
		AuthType:               cmp.Or(strings.TrimSpace(cfg.AuthType), defaultAuth),
		PollPath:               strings.TrimSpace(cfg.PollPath),
		IsPassthrough:          isPassthrough,
		AssetConvert:           cfg.AssetConvert,
		AssetConvertNS:         cmp.Or(strings.TrimSpace(cfg.AssetConvertNS), "asset_manager"),
		UpstreamAssetConvert:   cfg.UpstreamAssetConvert,
		UpstreamAssetBindingID: cfg.UpstreamAssetBindingID,
		AssetModeration:        cfg.Moderation,
		ContentToPrompt:        cfg.ContentToPrompt,
		FastResponse:           cfg.FastResponse,
		AllowedResolutions:     slices.Clone(cfg.AllowedResolutions),
		AllowedQualities:       slices.Clone(cfg.AllowedQualities),
		MaxRefImages:           cfg.MaxRefImages,
		MinPixels:              cfg.MinPixels,
		MaxPixels:              cfg.MaxPixels,
		AutoDowngrade:          cfg.AutoDowngrade,
		Step:                   cfg.Step,
		MaxAspectRatio:         cfg.MaxAspectRatio,
		Raw:                    maps.Clone(raw),
	}
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
	trimmed := cmp.Or(strings.TrimSpace(configJSON), "{}")
	var cfg ForwardConfig
	if err := json.Unmarshal([]byte(trimmed), &cfg); err != nil {
		return ResolvedForward{}, fmt.Errorf("parse forward config_json: %w", err)
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &raw); err != nil {
		raw = map[string]json.RawMessage{}
	}
	return buildResolvedForward(cfg, raw, categoryPath, requestPath, eid, ruleID, ruleName, model), nil
}

// ValidateConfigJSON 校验转发规则 config_json 语法、鉴权白名单与路径安全性（防配置注入/SSRF）。
func ValidateConfigJSON(configJSON string) error {
	trimmed := strings.TrimSpace(configJSON)
	if trimmed == "" || trimmed == "{}" {
		return nil
	}
	var cfg ForwardConfig
	if err := json.Unmarshal([]byte(trimmed), &cfg); err != nil {
		return fmt.Errorf("invalid config_json syntax: %w", err)
	}
	if err := ValidateAuthType(cfg.AuthType); err != nil {
		return err
	}
	if err := ValidatePathTemplate(cfg.PollPath); err != nil {
		return fmt.Errorf("invalid poll_path: %w", err)
	}
	for _, rw := range CollectRewrites(cfg) {
		if err := ValidatePathTemplate(rw.Old); err != nil {
			return fmt.Errorf("invalid path_rewrite.old: %w", err)
		}
		if err := ValidatePathTemplate(rw.New); err != nil {
			return fmt.Errorf("invalid path_rewrite.new: %w", err)
		}
	}
	for _, p := range cfg.PathPassthrough {
		if err := ValidatePathTemplate(p); err != nil {
			return fmt.Errorf("invalid path_passthrough: %w", err)
		}
	}
	for _, p := range cfg.PathAccept {
		if err := ValidatePathTemplate(p); err != nil {
			return fmt.Errorf("invalid path_accept: %w", err)
		}
	}
	return nil
}

func scoreForwardConfig(cfg ForwardConfig, openaiClean, requestPath string, isOpenAIReq bool) int {
	if pathListHit(cfg.PathPassthrough, requestPath) || pathListHit(cfg.PathAccept, requestPath) {
		return 3
	}
	score := 0
	for _, p := range CollectRewrites(cfg) {
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
	return score
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
	candidates := make([]ForwardRule, 0, len(rules))
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
		trimmed := cmp.Or(strings.TrimSpace(r.ConfigJSON), "{}")
		var cfg ForwardConfig
		if json.Unmarshal([]byte(trimmed), &cfg) != nil {
			continue
		}
		score := scoreForwardConfig(cfg, openaiClean, requestPath, isOpenAIReq)
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

type cachedRuleEntry struct {
	rule ForwardRule
	cfg  ForwardConfig
	raw  map[string]json.RawMessage
}

type cachedBindingEntry struct {
	ruleIDs  []uint64
	isActive bool
}

// Resolver 提供防缓存击穿、预解析零热路径反序列化的模型转发规则解析器（零值安全可用）。
type Resolver struct {
	ttl    time.Duration
	Now    func() time.Time
	loadMu sync.Mutex // 串行化冷加载/过期刷新，防止并发流量击穿数据库；绝不在持 r.mu 时做 DB I/O
	mu     sync.RWMutex

	loadedAt        time.Time
	bindingsByModel map[string]cachedBindingEntry
	rulesByID       map[uint64]cachedRuleEntry
	rulesByEID      map[string]cachedRuleEntry
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
		Now:             time.Now,
		bindingsByModel: make(map[string]cachedBindingEntry),
		rulesByID:       make(map[uint64]cachedRuleEntry),
		rulesByEID:      make(map[string]cachedRuleEntry),
	}
}

func (r *Resolver) now() time.Time {
	if r != nil && r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Resolver) effectiveTTL() time.Duration {
	if r != nil && r.ttl > 0 {
		return r.ttl
	}
	return 15 * time.Second
}

// Invalidate 清空内存缓存（在后台 CRUD 或 Redis 广播到达时调用）。
func (r *Resolver) Invalidate() {
	if r == nil {
		return
	}
	r.mu.Lock()
	r.loadedAt = time.Time{}
	r.mu.Unlock()
}

func (r *Resolver) isFresh(now time.Time) bool {
	r.mu.RLock()
	fresh := !r.loadedAt.IsZero() && now.Sub(r.loadedAt) < r.effectiveTTL()
	r.mu.RUnlock()
	return fresh
}

func (r *Resolver) ensureLoaded(ctx context.Context, db *gorm.DB) error {
	now := r.now()
	if r.isFresh(now) {
		return nil
	}

	r.loadMu.Lock()
	defer r.loadMu.Unlock()

	now = r.now()
	if r.isFresh(now) {
		return nil
	}

	var bindings []bindingRow
	if err := db.WithContext(ctx).
		Table("model_rule_bindings").
		Select("model, forward_rule_ids, is_active").
		Where("is_active = 1").
		Find(&bindings).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("load model_rule_bindings: %w", err)
	}

	var rules []ForwardRule
	if err := db.WithContext(ctx).
		Where("is_active = 1").
		Order("sort_order ASC, id ASC").
		Find(&rules).Error; err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("load forward_rules: %w", err)
	}

	bMap := make(map[string]cachedBindingEntry, len(bindings))
	for _, b := range bindings {
		var ids []uint64
		_ = json.Unmarshal([]byte(b.ForwardRuleIDs), &ids)
		bMap[b.Model] = cachedBindingEntry{
			ruleIDs:  ids,
			isActive: b.IsActive == 1,
		}
	}

	rByID := make(map[uint64]cachedRuleEntry, len(rules))
	rByEID := make(map[string]cachedRuleEntry, len(rules))
	for _, fr := range rules {
		trimmed := cmp.Or(strings.TrimSpace(fr.ConfigJSON), "{}")
		var cfg ForwardConfig
		if json.Unmarshal([]byte(trimmed), &cfg) != nil {
			continue
		}
		var raw map[string]json.RawMessage
		if json.Unmarshal([]byte(trimmed), &raw) != nil {
			raw = map[string]json.RawMessage{}
		}
		entry := cachedRuleEntry{rule: fr, cfg: cfg, raw: raw}
		rByID[fr.ID] = entry
		if fr.EID != "" {
			rByEID[fr.EID] = entry
		}
	}

	r.mu.Lock()
	r.bindingsByModel = bMap
	r.rulesByID = rByID
	r.rulesByEID = rByEID
	r.loadedAt = now
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
	if r == nil || db == nil || strings.TrimSpace(model) == "" {
		return nil, nil
	}
	if err := r.ensureLoaded(ctx, db); err != nil {
		return nil, err
	}

	r.mu.RLock()
	b, ok := r.bindingsByModel[model]
	if !ok || !b.isActive || len(b.ruleIDs) == 0 {
		r.mu.RUnlock()
		return nil, nil
	}
	boundEntries := make([]cachedRuleEntry, 0, len(b.ruleIDs))
	for _, id := range b.ruleIDs {
		if entry, exists := r.rulesByID[id]; exists && entry.rule.IsActive == 1 {
			boundEntries = append(boundEntries, entry)
		}
	}
	r.mu.RUnlock()

	if len(boundEntries) == 0 {
		return nil, nil
	}

	cat := strings.TrimSpace(category)
	candidates := make([]cachedRuleEntry, 0, len(boundEntries))
	if cat != "" {
		for _, e := range boundEntries {
			if e.rule.Category == cat || e.rule.Category == "封装" {
				candidates = append(candidates, e)
			}
		}
	}
	if len(candidates) == 0 {
		candidates = boundEntries
	}

	openaiPath := CategoryEndpoint(cat)
	openaiClean := strings.TrimLeft(openaiPath, "/")
	reqClean := strings.TrimLeft(strings.TrimSpace(requestPath), "/")
	isOpenAIReq := reqClean == openaiClean || strings.HasSuffix(reqClean, openaiClean)

	var best *cachedRuleEntry
	bestScore := 0
	for i := range candidates {
		score := scoreForwardConfig(candidates[i].cfg, openaiClean, requestPath, isOpenAIReq)
		if score > bestScore {
			bestScore = score
			best = &candidates[i]
			if bestScore == 3 {
				break
			}
		}
	}
	if best == nil || bestScore == 0 {
		return nil, nil
	}

	resolved := buildResolvedForward(
		best.cfg,
		best.raw,
		openaiPath,
		requestPath,
		best.rule.EID,
		best.rule.ID,
		best.rule.Name,
		model,
	)
	return &resolved, nil
}

// ResolveByEID 按转发规则 EID 从内存快照快速解析（用于异步任务轮询，免去每轮查库）。
func (r *Resolver) ResolveByEID(
	ctx context.Context,
	db *gorm.DB,
	eid string,
	categoryPath string,
	model string,
) (*ResolvedForward, error) {
	eid = strings.TrimSpace(eid)
	if r == nil || db == nil || eid == "" {
		return nil, nil
	}
	if err := r.ensureLoaded(ctx, db); err != nil {
		return nil, err
	}
	r.mu.RLock()
	entry, ok := r.rulesByEID[eid]
	r.mu.RUnlock()
	if !ok {
		return nil, nil
	}
	resolved := buildResolvedForward(
		entry.cfg,
		entry.raw,
		cmp.Or(categoryPath, "/v1/videos"),
		"",
		entry.rule.EID,
		entry.rule.ID,
		entry.rule.Name,
		model,
	)
	return &resolved, nil
}
