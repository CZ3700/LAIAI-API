package server

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

const (
	maxUpstreamSyncLogsPerConfig = 200
	defaultUpstreamCurrencyRate  = 7.2
)

type channelConfigSyncLogRow struct {
	ID                  uint64    `gorm:"primaryKey;autoIncrement"`
	ConfigID            uint64    `gorm:"column:config_id"`
	UpstreamSystem      string    `gorm:"column:upstream_system"`
	UpstreamGroup       string    `gorm:"column:upstream_group"`
	UpstreamRatio       float64   `gorm:"column:upstream_ratio"`
	RateAdd             float64   `gorm:"column:rate_add"`
	UpstreamCurrency    string    `gorm:"column:upstream_currency"`
	SiteCurrency        string    `gorm:"column:site_currency"`
	CurrencyRate        float64   `gorm:"column:currency_rate"`
	PreviousRate        float64   `gorm:"column:previous_rate"`
	AppliedRate         float64   `gorm:"column:applied_rate"`
	SyncIntervalMinutes int       `gorm:"column:sync_interval_minutes"`
	TriggerType         string    `gorm:"column:trigger_type"`
	Status              int8      `gorm:"column:status"`
	ErrorMessage        string    `gorm:"column:error_message"`
	CreatedAt           time.Time `gorm:"column:created_at"`
}

func (channelConfigSyncLogRow) TableName() string { return "channel_config_sync_logs" }

type v1ChannelConfigSyncLog struct {
	ID                  uint64  `json:"id"`
	ConfigID            uint64  `json:"config_id"`
	UpstreamSystem      string  `json:"upstream_system"`
	UpstreamGroup       string  `json:"upstream_group"`
	UpstreamRatio       float64 `json:"upstream_ratio"`
	RateAdd             float64 `json:"rate_add"`
	UpstreamCurrency    string  `json:"upstream_currency"`
	SiteCurrency        string  `json:"site_currency"`
	CurrencyRate        float64 `json:"currency_rate"`
	PreviousRate        float64 `json:"previous_rate"`
	AppliedRate         float64 `json:"applied_rate"`
	SyncIntervalMinutes int     `json:"sync_interval_minutes"`
	TriggerType         string  `json:"trigger_type"`
	Status              int8    `json:"status"`
	ErrorMessage        string  `json:"error_message"`
	CreatedAt           string  `json:"created_at"`
}

func (r channelConfigSyncLogRow) toV1() v1ChannelConfigSyncLog {
	return v1ChannelConfigSyncLog{
		ID:                  r.ID,
		ConfigID:            r.ConfigID,
		UpstreamSystem:      r.UpstreamSystem,
		UpstreamGroup:       r.UpstreamGroup,
		UpstreamRatio:       r.UpstreamRatio,
		RateAdd:             r.RateAdd,
		UpstreamCurrency:    r.UpstreamCurrency,
		SiteCurrency:        r.SiteCurrency,
		CurrencyRate:        r.CurrencyRate,
		PreviousRate:        r.PreviousRate,
		AppliedRate:         r.AppliedRate,
		SyncIntervalMinutes: r.SyncIntervalMinutes,
		TriggerType:         r.TriggerType,
		Status:              r.Status,
		ErrorMessage:        r.ErrorMessage,
		CreatedAt:           r.CreatedAt.UTC().Format(time.RFC3339),
	}
}

// UpstreamGroupOption 描述上游（如 NewAPI）返回的可用分组及其基础倍率。
type UpstreamGroupOption struct {
	Name        string  `json:"name"`
	Ratio       float64 `json:"ratio"`
	Description string  `json:"description,omitempty"`
}

func isMaskedSecret(s string) bool {
	t := strings.TrimSpace(s)
	return strings.HasPrefix(t, "••••") || strings.HasPrefix(t, "****")
}

// NormalizeNewAPIOrigin 归一化 NewAPI/OneAPI 站点根地址（去掉末尾 /、/v1、/v1beta、/api/v3、/api 等后缀）。
func NormalizeNewAPIOrigin(baseURL string) string {
	trimmed := strings.TrimSpace(baseURL)
	if trimmed == "" {
		return ""
	}
	if !strings.HasPrefix(trimmed, "http://") && !strings.HasPrefix(trimmed, "https://") {
		trimmed = "https://" + trimmed
	}
	origin := strings.TrimRight(trimmed, "/")
	for {
		lower := strings.ToLower(origin)
		changed := false
		for _, suffix := range []string{"/api/v3", "/api/v1", "/openai/v1", "/v1beta", "/v1", "/api"} {
			if strings.HasSuffix(lower, suffix) {
				origin = strings.TrimRight(origin[:len(origin)-len(suffix)], "/")
				changed = true
				break
			}
		}
		if !changed {
			break
		}
	}
	return origin
}

// ConvertCurrencyRatio 按上游币种与站点币种换算基础倍率。
func ConvertCurrencyRatio(ratio float64, upCurr, siteCurr string, currRate float64) float64 {
	if currRate <= 0 || math.IsNaN(currRate) || math.IsInf(currRate, 0) {
		currRate = defaultUpstreamCurrencyRate
	}
	up := strings.ToUpper(strings.TrimSpace(upCurr))
	st := strings.ToUpper(strings.TrimSpace(siteCurr))
	switch {
	case up == "CNY" && st == "USD":
		return ratio / currRate
	case up == "USD" && st == "CNY":
		return ratio * currRate
	default:
		return ratio
	}
}

// AppliedChannelRate 计算同步后的最终采购倍率（保留 6 位小数）。
func AppliedChannelRate(ratio, add float64, upCurr, siteCurr string, currRate float64) float64 {
	converted := ConvertCurrencyRatio(ratio, upCurr, siteCurr, currRate)
	raw := max(0, converted+add)
	return math.Round(raw*1e6) / 1e6
}

func parseFlexibleNumber(raw any) (float64, bool) {
	switch v := raw.(type) {
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return 0, false
		}
		return v, true
	case int:
		return float64(v), true
	case int64:
		return float64(v), true
	case json.Number:
		f, err := v.Float64()
		return f, err == nil && !math.IsNaN(f) && !math.IsInf(f, 0)
	case string:
		f, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return f, err == nil && !math.IsNaN(f) && !math.IsInf(f, 0)
	default:
		return 0, false
	}
}

// ParseNewAPIGroups 解析 NewAPI /api/pricing 或 /api/user/self/groups 返回的分组倍率字典。
func ParseNewAPIGroups(raw []byte) []UpstreamGroupOption {
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return []UpstreamGroupOption{}
	}

	usable := map[string]string{}
	extractUsable := func(obj map[string]any) {
		if m, ok := obj["usable_group"].(map[string]any); ok {
			for k, v := range m {
				if s, ok := v.(string); ok {
					usable[k] = strings.TrimSpace(s)
				}
			}
		}
	}
	extractUsable(root)

	ratios := map[string]float64{}
	collectRatioMap := func(m map[string]any) {
		for k, v := range m {
			name := strings.TrimSpace(k)
			if name == "" {
				continue
			}
			if r, ok := parseFlexibleNumber(v); ok {
				ratios[name] = r
			}
		}
	}

	if gr, ok := root["group_ratio"].(map[string]any); ok {
		collectRatioMap(gr)
	}
	if dataMap, ok := root["data"].(map[string]any); ok {
		extractUsable(dataMap)
		if gr, ok := dataMap["group_ratio"].(map[string]any); ok {
			collectRatioMap(gr)
		} else {
			// /api/user/self/groups 格式: {"data": {"default": {"ratio": 1, "desc": "默认分组"}, ...}}
			for k, v := range dataMap {
				name := strings.TrimSpace(k)
				if name == "" {
					continue
				}
				switch item := v.(type) {
				case map[string]any:
					if r, ok := parseFlexibleNumber(item["ratio"]); ok {
						ratios[name] = r
						desc := cmp.Or(stringVal(item["desc"]), stringVal(item["description"]), stringVal(item["name"]))
						if desc != "" && usable[name] == "" {
							usable[name] = desc
						}
					}
				default:
					if r, ok := parseFlexibleNumber(item); ok {
						ratios[name] = r
					}
				}
			}
		}
	}

	out := make([]UpstreamGroupOption, 0, len(ratios))
	for name, ratio := range ratios {
		out = append(out, UpstreamGroupOption{
			Name:        name,
			Ratio:       ratio,
			Description: usable[name],
		})
	}
	slices.SortFunc(out, func(a, b UpstreamGroupOption) int {
		if a.Name == "default" && b.Name != "default" {
			return -1
		}
		if b.Name == "default" && a.Name != "default" {
			return 1
		}
		return cmp.Compare(a.Name, b.Name)
	})
	return out
}

func stringVal(v any) string {
	s, _ := v.(string)
	return strings.TrimSpace(s)
}

func (s *Server) validateUpstreamTargetURL(rawURL string) error {
	allowPrivate := false
	if s.db != nil {
		allowPrivate = relay.AllowPrivateFromDB(s.db)
	}
	if err := relay.ValidateAdvancedUpstreamURL(rawURL, allowPrivate); err != nil {
		return err
	}
	if !allowPrivate {
		if u, err := url.Parse(strings.TrimSpace(rawURL)); err == nil {
			h := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
			if h == "localhost" || strings.HasSuffix(h, ".localhost") || strings.HasSuffix(h, ".local") || strings.HasSuffix(h, ".internal") {
				return errors.New("private/local hostname is blocked")
			}
		}
	}
	return nil
}

func (s *Server) upstreamHTTPClient() *http.Client {
	if s.upstreamHTTP != nil {
		return s.upstreamHTTP
	}
	allowPrivate := false
	if s.db != nil {
		allowPrivate = relay.AllowPrivateFromDB(s.db)
	}
	// Honors outbound HTTP_PROXY/HTTPS_PROXY when configured; otherwise enforces direct SSRF dial guard.
	transport := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	cl := relay.NewGuardedClient(20*time.Second, allowPrivate)
	if baseTr, ok := cl.Transport.(*http.Transport); ok && baseTr != nil {
		hasEnvProxy := false
		if req, err := http.NewRequest(http.MethodGet, "https://example.com", nil); err == nil {
			if p, perr := http.ProxyFromEnvironment(req); perr == nil && p != nil {
				hasEnvProxy = true
			}
		}
		if hasEnvProxy {
			cl.Transport = transport
		} else {
			baseTr.TLSHandshakeTimeout = 10 * time.Second
		}
	}
	origCheck := cl.CheckRedirect
	cl.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if err := s.validateUpstreamTargetURL(req.URL.String()); err != nil {
			return err
		}
		if origCheck != nil {
			return origCheck(req, via)
		}
		return nil
	}
	return cl
}

// FetchNewAPIGroups 拉取 NewAPI 上游分组列表（优先在填写 token+user_id 时调用 /api/user/self/groups，否则免 Key 探测 /api/pricing）。
func (s *Server) FetchNewAPIGroups(ctx context.Context, baseURL, apiKey, upstreamUserID, upstreamAccessToken string) ([]UpstreamGroupOption, error) {
	origin := NormalizeNewAPIOrigin(baseURL)
	if origin == "" {
		return nil, errors.New("请先填写有效的上游基础地址")
	}
	if relay.IsMetadataURL(origin) {
		return nil, errors.New("不允许访问云元数据地址")
	}
	if s.upstreamHTTP == nil {
		if err := s.validateUpstreamTargetURL(origin); err != nil {
			return nil, fmt.Errorf("上游地址未通过安全校验: %w", err)
		}
	}

	uid := strings.TrimSpace(upstreamUserID)
	tok := strings.TrimSpace(upstreamAccessToken)
	if isMaskedSecret(tok) {
		tok = ""
	}
	if (tok != "" && uid == "") || (uid != "" && tok == "") {
		return nil, errors.New("使用系统访问令牌拉取私有分组时，必须同时填写 NewAPI 用户 ID 和系统访问令牌")
	}

	cl := s.upstreamHTTPClient()
	doFetch := func(targetURL string, bearer string, userHeader string) ([]byte, int, error) {
		reqCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
		defer cancel()
		req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, targetURL, nil)
		if err != nil {
			return nil, 0, fmt.Errorf("build upstream request: %w", err)
		}
		req.Header.Set("Accept", "application/json")
		req.Header.Set("User-Agent", "LAIAI-Gateway/2.0")
		if bearer != "" && !isMaskedSecret(bearer) {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		if userHeader != "" {
			req.Header.Set("New-Api-User", userHeader)
		}
		resp, err := cl.Do(req)
		if err != nil {
			return nil, 0, fmt.Errorf("请求上游失败: %w", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		return body, resp.StatusCode, nil
	}

	if tok != "" && uid != "" {
		body, status, err := doFetch(origin+"/api/user/self/groups", tok, uid)
		if err != nil {
			return nil, err
		}
		if status < 200 || status >= 300 {
			return nil, fmt.Errorf("上游 /api/user/self/groups 返回 HTTP %d", status)
		}
		groups := ParseNewAPIGroups(body)
		if len(groups) > 0 {
			return groups, nil
		}
	}

	body, status, err := doFetch(origin+"/api/pricing", strings.TrimSpace(apiKey), "")
	if err != nil {
		return nil, err
	}
	if status < 200 || status >= 300 {
		return nil, fmt.Errorf("上游 /api/pricing 返回 HTTP %d", status)
	}
	groups := ParseNewAPIGroups(body)
	return groups, nil
}

func (s *Server) decryptConfigSecret(enc string) string {
	trimmed := strings.TrimSpace(enc)
	if trimmed == "" {
		return ""
	}
	if s.keyring != nil && strings.HasPrefix(trimmed, "enc:v1:") {
		if plain, err := s.keyring.Decrypt(trimmed); err == nil {
			return plain
		}
	}
	return trimmed
}

func (s *Server) v1FetchChannelConfigUpstreamGroups(c *gin.Context) {
	var req struct {
		ConfigID            uint64 `json:"config_id"`
		BaseURL             string `json:"base_url"`
		APIKey              string `json:"api_key"`
		UpstreamSystem      string `json:"upstream_system"`
		UpstreamUserID      string `json:"upstream_user_id"`
		UpstreamAccessToken string `json:"upstream_access_token"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid request body")
		return
	}
	sys := cmp.Or(strings.TrimSpace(req.UpstreamSystem), "newapi")
	if !strings.EqualFold(sys, "newapi") {
		fail(c, http.StatusBadRequest, "当前仅支持 NewAPI 上游系统拉取分组倍率")
		return
	}

	baseURL := strings.TrimSpace(req.BaseURL)
	apiKey := strings.TrimSpace(req.APIKey)
	uid := strings.TrimSpace(req.UpstreamUserID)
	tok := strings.TrimSpace(req.UpstreamAccessToken)

	if req.ConfigID > 0 {
		var row channelConfigRow
		if err := s.db.WithContext(c.Request.Context()).First(&row, req.ConfigID).Error; err == nil {
			if baseURL == "" {
				baseURL = row.BaseURL
			}
			if apiKey == "" || isMaskedSecret(apiKey) {
				apiKey = s.decryptConfigSecret(row.APIKeyEnc)
			}
			if uid == "" {
				uid = row.UpstreamUserID
			}
			if tok == "" || isMaskedSecret(tok) {
				tok = s.decryptConfigSecret(row.UpstreamAccessTokenEnc)
			}
		}
	}

	groups, err := s.FetchNewAPIGroups(c.Request.Context(), baseURL, apiKey, uid, tok)
	if err != nil {
		fail(c, http.StatusBadGateway, err.Error())
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": groups})
}

func (s *Server) pruneChannelConfigSyncLogs(ctx context.Context, configID uint64) {
	var ids []uint64
	_ = s.db.WithContext(ctx).Model(&channelConfigSyncLogRow{}).
		Where("config_id = ?", configID).
		Order("created_at DESC, id DESC").
		Offset(maxUpstreamSyncLogsPerConfig).
		Pluck("id", &ids).Error
	if len(ids) > 0 {
		_ = s.db.WithContext(ctx).Delete(&channelConfigSyncLogRow{}, ids).Error
	}
}

func (s *Server) syncSingleChannelConfigRate(ctx context.Context, id uint64, triggerType string) (*channelConfigRow, *channelConfigSyncLogRow, error) {
	var row channelConfigRow
	if err := s.db.WithContext(ctx).First(&row, id).Error; err != nil {
		return nil, nil, err
	}
	if !strings.EqualFold(strings.TrimSpace(row.UpstreamSystem), "newapi") {
		return nil, nil, errors.New("仅 newapi 上游系统支持同步分组倍率")
	}
	groupName := strings.TrimSpace(row.UpstreamGroup)
	if groupName == "" {
		return nil, nil, errors.New("未选择上游同步分组")
	}

	upCurr := cmp.Or(strings.TrimSpace(row.UpstreamCurrency), "CNY")
	stCurr := cmp.Or(strings.TrimSpace(row.SiteCurrency), "USD")
	currRate := row.UpstreamCurrencyRate
	if currRate <= 0 {
		currRate = cmp.Or(row.CurrencyRate, defaultUpstreamCurrencyRate)
	}
	now := s.now().UTC()

	recordFailure := func(errMsg string) (*channelConfigRow, *channelConfigSyncLogRow, error) {
		logEntry := channelConfigSyncLogRow{
			ConfigID:            row.ID,
			UpstreamSystem:      row.UpstreamSystem,
			UpstreamGroup:       groupName,
			RateAdd:             row.UpstreamSyncRateAdd,
			UpstreamCurrency:    upCurr,
			SiteCurrency:        stCurr,
			CurrencyRate:        currRate,
			PreviousRate:        row.Rate,
			AppliedRate:         row.Rate,
			SyncIntervalMinutes: row.UpstreamSyncIntervalMinutes,
			TriggerType:         cmp.Or(triggerType, "manual"),
			Status:              0,
			ErrorMessage:        errMsg,
			CreatedAt:           now,
		}
		_ = s.db.WithContext(ctx).Create(&logEntry).Error
		s.pruneChannelConfigSyncLogs(ctx, row.ID)
		return &row, &logEntry, errors.New(errMsg)
	}

	apiKey := s.decryptConfigSecret(row.APIKeyEnc)
	accessTok := s.decryptConfigSecret(row.UpstreamAccessTokenEnc)
	groups, err := s.FetchNewAPIGroups(ctx, row.BaseURL, apiKey, row.UpstreamUserID, accessTok)
	if err != nil {
		return recordFailure(err.Error())
	}
	var matched *UpstreamGroupOption
	for i := range groups {
		if groups[i].Name == groupName {
			matched = &groups[i]
			break
		}
	}
	if matched == nil {
		return recordFailure(fmt.Sprintf("上游分组列表未包含分组 %q", groupName))
	}

	applied := AppliedChannelRate(matched.Ratio, row.UpstreamSyncRateAdd, upCurr, stCurr, currRate)
	prevRate := row.Rate

	if err := s.db.WithContext(ctx).Model(&channelConfigRow{}).Where("id = ?", row.ID).Updates(map[string]any{
		"rate":                   applied,
		"upstream_currency":      upCurr,
		"site_currency":          stCurr,
		"upstream_currency_rate": currRate,
		"currency_rate":          currRate,
		"upstream_synced_at":     now,
		"updated_at":             now,
	}).Error; err != nil {
		return recordFailure("更新上游配置倍率失败: " + err.Error())
	}

	logEntry := channelConfigSyncLogRow{
		ConfigID:            row.ID,
		UpstreamSystem:      row.UpstreamSystem,
		UpstreamGroup:       groupName,
		UpstreamRatio:       matched.Ratio,
		RateAdd:             row.UpstreamSyncRateAdd,
		UpstreamCurrency:    upCurr,
		SiteCurrency:        stCurr,
		CurrencyRate:        currRate,
		PreviousRate:        prevRate,
		AppliedRate:         applied,
		SyncIntervalMinutes: row.UpstreamSyncIntervalMinutes,
		TriggerType:         cmp.Or(triggerType, "manual"),
		Status:              1,
		CreatedAt:           now,
	}
	_ = s.db.WithContext(ctx).Create(&logEntry).Error
	s.pruneChannelConfigSyncLogs(ctx, row.ID)

	// 将最新采购倍率同步到直接绑定此预设（非 HA 虚拟组）的模型渠道。
	metaMap := s.loadV1ChannelMetaMap()
	for chID, meta := range metaMap {
		if meta.PresetID != nil && *meta.PresetID == row.ID && meta.RawProviderType != "high_availability_group" && applied > 0 {
			_ = s.db.WithContext(ctx).Model(&relay.Channel{}).
				Where("id = ?", chID).
				Update("upstream_cost_ratio", decimal.NewFromFloat(applied)).Error
		}
	}
	s.invalidateCache(relay.KindChannel, "")

	_ = s.db.WithContext(ctx).First(&row, row.ID).Error
	return &row, &logEntry, nil
}

func (s *Server) v1TriggerChannelConfigSyncRate(c *gin.Context) {
	id, ok := parseUintIDParam(c, "id")
	if !ok {
		return
	}
	row, logEntry, err := s.syncSingleChannelConfigRate(c.Request.Context(), id, "manual")
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			fail(c, http.StatusNotFound, "channel config not found")
			return
		}
		fail(c, http.StatusBadGateway, err.Error())
		return
	}
	dto := row.toV1()
	dto.APIKeyEnc = ""
	var syncLogDTO any
	if logEntry != nil {
		syncLogDTO = logEntry.toV1()
	}
	c.JSON(http.StatusOK, gin.H{
		"synced":   true,
		"data":     dto,
		"sync_log": syncLogDTO,
	})
}

func (s *Server) v1ListChannelConfigSyncLogs(c *gin.Context) {
	id, ok := parseUintIDParam(c, "id")
	if !ok {
		return
	}
	limit := 50
	if rawLimit := strings.TrimSpace(c.Query("limit")); rawLimit != "" {
		if n, err := strconv.Atoi(rawLimit); err == nil && n > 0 {
			limit = min(n, maxUpstreamSyncLogsPerConfig)
		}
	}
	var rows []channelConfigSyncLogRow
	if err := s.readOnly().WithContext(c.Request.Context()).
		Where("config_id = ?", id).
		Order("created_at DESC, id DESC").
		Limit(limit).
		Find(&rows).Error; err != nil {
		fail(c, http.StatusInternalServerError, "list sync logs failed")
		return
	}
	out := make([]v1ChannelConfigSyncLog, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.toV1())
	}
	c.JSON(http.StatusOK, gin.H{"data": out})
}

// SyncUpstreamChannelRates 周期性检查并自动同步开启定时的 NewAPI 上游分组倍率。
func (s *Server) SyncUpstreamChannelRates(ctx context.Context) int {
	var rows []channelConfigRow
	if err := s.db.WithContext(ctx).
		Where("status = 1 AND LOWER(upstream_system) = ? AND upstream_group <> '' AND upstream_sync_interval_minutes > 0", "newapi").
		Find(&rows).Error; err != nil {
		return 0
	}
	now := s.now().UTC()
	synced := 0
	for _, row := range rows {
		if ctx.Err() != nil {
			break
		}
		interval := time.Duration(row.UpstreamSyncIntervalMinutes) * time.Minute
		if row.UpstreamSyncedAt != nil && now.Sub(*row.UpstreamSyncedAt) < interval {
			continue
		}
		if _, _, err := s.syncSingleChannelConfigRate(ctx, row.ID, "auto"); err == nil {
			synced++
		}
	}
	return synced
}

func (s *Server) v1ChannelConfigsVolcBalance(c *gin.Context) {
	var req struct {
		ConfigID uint64 `json:"config_id"`
		VolcAK   string `json:"volc_ak"`
		VolcSK   string `json:"volc_sk"`
	}
	_ = c.ShouldBindJSON(&req)
	ak := strings.TrimSpace(req.VolcAK)
	sk := strings.TrimSpace(req.VolcSK)
	if isMaskedSecret(sk) {
		sk = ""
	}
	ctx := c.Request.Context()
	if req.ConfigID > 0 {
		var row channelConfigRow
		if err := s.db.WithContext(ctx).First(&row, req.ConfigID).Error; err == nil {
			if ak == "" {
				ak = row.VolcAK
			}
			if sk == "" {
				sk = s.decryptConfigSecret(row.VolcSKEnc)
			}
		}
	}
	if ak == "" || sk == "" {
		c.JSON(http.StatusOK, gin.H{
			"configured":        false,
			"available_balance": 0.0,
			"currency":          "CNY",
			"message":           "请先配置火山引擎主账号 AK / SK 后查询实时余额",
		})
		return
	}

	cl := ark.NewClient(ark.ClientConfig{
		AccessKey:       ak,
		SecretKey:       sk,
		Region:          ark.DefaultVolcRegion,
		BaseURLOverride: s.arkOpenAIOverride,
	}, s.arkHTTPClient)
	info, err := cl.QueryBalanceDetail(ctx)
	if err != nil {
		fail(c, http.StatusBadGateway, "查询火山账户余额失败: "+err.Error())
		return
	}
	now := s.now().UTC()
	if req.ConfigID > 0 {
		_ = s.db.WithContext(ctx).Model(&channelConfigRow{}).Where("id = ?", req.ConfigID).Updates(map[string]any{
			"upstream_balance":            info.AvailableBalance,
			"upstream_balance_updated_at": now,
			"updated_at":                  now,
		}).Error
	}
	c.JSON(http.StatusOK, gin.H{
		"configured":                  true,
		"available_balance":           info.AvailableBalance,
		"cash_balance":                info.CashBalance,
		"freeze_amount":               info.FreezeAmount,
		"arrears_balance":             info.ArrearsBalance,
		"credit_limit":                info.CreditLimit,
		"currency":                    "CNY",
		"upstream_balance_updated_at": now.Format(time.RFC3339),
	})
}

func (s *Server) collectBoundChannelsByConfigID(ctx context.Context) map[uint64][]v1BoundChannelSummary {
	out := map[uint64][]v1BoundChannelSummary{}
	var channels []relay.Channel
	if err := s.readOnly().WithContext(ctx).
		Select("id, name, priority, weight, status, owner_org_id").
		Where("owner_org_id = 0").
		Order("id ASC").
		Find(&channels).Error; err != nil {
		return out
	}
	metaMap := s.loadV1ChannelMetaMap()
	for _, ch := range channels {
		meta := metaMap[ch.ID]
		isHA := meta.RawProviderType == "high_availability_group"
		aid := cmp.Or(meta.GroupAID, "A"+strconv.FormatUint(ch.ID, 10))
		attached := map[uint64]struct{}{}
		if meta.PresetID != nil && *meta.PresetID > 0 {
			attached[*meta.PresetID] = struct{}{}
		}
		if len(meta.Config) > 0 {
			var parsed struct {
				SubChannels []uint64 `json:"sub_channels"`
			}
			if json.Unmarshal(meta.Config, &parsed) == nil {
				for _, sid := range parsed.SubChannels {
					if sid > 0 {
						attached[sid] = struct{}{}
					}
				}
			}
		}
		for cfgID := range attached {
			out[cfgID] = append(out[cfgID], v1BoundChannelSummary{
				ID:                ch.ID,
				Name:              ch.Name,
				GroupAID:          aid,
				IsHA:              isHA,
				Status:            ch.Status,
				EffectivePriority: ch.Priority,
				EffectiveWeight:   ch.Weight,
			})
		}
	}
	return out
}
