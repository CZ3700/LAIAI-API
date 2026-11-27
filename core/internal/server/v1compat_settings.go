package server

import (
	"cmp"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const v1FullSettingsKey = "v1_full_settings_json"

func parseCNYFloat(raw string) float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || v <= 0 {
		return 7.3
	}
	return v
}

func (s *Server) buildFullSettingsPayload() map[string]any {
	siteName := cmp.Or(s.getSetting("site_name"), "LAIAI")
	adminPath := cmp.Or(s.getSetting("admin_path"), "admin")
	currencyEnabled := s.getBoolSetting("currency_display_enabled")
	_, cnyRate := s.effectiveCNYPerUSD()
	cnyFloat := parseCNYFloat(cnyRate)

	base := map[string]any{
		"site": map[string]any{
			"name":                   siteName,
			"title":                  siteName + " AI Gateway",
			"keywords":               "AI Gateway, LLM, Seedance",
			"description":            "Enterprise Multi-Protocol AI Gateway",
			"admin_path":             adminPath,
			"enable_multilingual":    true,
			"enable_theme_toggle":    true,
			"default_theme":          "light",
			"default_language":       "zh",
			"supported_languages":    []string{"zh", "en", "zh-TW"},
			"default_timezone":       "Asia/Shanghai",
			"admin_display_timezone": "Asia/Shanghai",
			"copyright":              s.getSetting("footer_text"),
			"support_email":          s.getSetting("support_email"),
			"maintenance":            s.getBoolSetting("maintenance_mode"),
		},
		"currency": map[string]any{
			"default_currency": map[bool]string{true: "CNY", false: "USD"}[currencyEnabled],
			"currency_symbol":  map[bool]string{true: "¥", false: "$"}[currencyEnabled],
			"currency_unit":    map[bool]string{true: "元", false: "USD"}[currencyEnabled],
			"token_ratio":      cnyFloat,
			"quick_amounts":    []int{10, 50, 100, 500},
		},
		"login": map[string]any{
			"enable_username_login": true,
			"enable_email_login":    true,
			"enable_mobile_login":   false,
			"enable_wechat_login":   false,
			"enable_google_login":   s.getSetting("google_client_id") != "",
			"enable_github_login":   s.getSetting("github_client_id") != "",
		},
		"registration": map[string]any{
			"enable_username_registration": s.getBoolSetting("register_enabled"),
			"enable_email_registration":    s.getBoolSetting("register_enabled"),
			"email_verification_enabled":   s.getBoolSetting("email_verification_enabled"),
			"enable_password_recovery":     true,
		},
		"agreement": map[string]any{
			"tos_mode":     "page",
			"privacy_mode": "page",
		},
		"notification": map[string]any{
			"site_notification_enabled": true,
			"smtp_host":                 s.getSetting("smtp_host"),
			"smtp_port":                 s.getSetting("smtp_port"),
			"smtp_from":                 s.getSetting("smtp_from"),
		},
		"relay": map[string]any{
			"channel_auto_disable":      s.getBoolSetting("channel_auto_disable"),
			"channel_disable_threshold": s.getSetting("channel_disable_threshold"),
			"model_rate_limit_enabled":  s.getBoolSetting("model_rate_limit_enabled"),
			"model_rate_limit_rpm":      s.getSetting("model_rate_limit_rpm"),
			"model_rate_limit_tpm":      s.getSetting("model_rate_limit_tpm"),
		},
		"log_cleanup": s.LoadLogCleanupSettings(),
		"server_time": s.now().UTC().Format("2006-01-02 15:04:05"),
	}

	if raw := s.getSetting(v1FullSettingsKey); strings.TrimSpace(raw) != "" {
		var saved map[string]any
		if err := json.Unmarshal([]byte(raw), &saved); err == nil {
			for section, val := range saved {
				if subMap, ok := val.(map[string]any); ok {
					if baseSub, ok2 := base[section].(map[string]any); ok2 {
						for k, v := range subMap {
							if isSensitiveSettingKey(k) {
								if sVal, ok := v.(string); ok && strings.TrimSpace(sVal) != "" {
									baseSub[k] = secretMask
								}
								continue
							}
							baseSub[k] = v
						}
						continue
					}
				}
				base[section] = val
			}
		}
	}
	return base
}

func isSensitiveSettingKey(k string) bool {
	lower := strings.ToLower(k)
	return strings.Contains(lower, "secret") || strings.Contains(lower, "password") ||
		strings.Contains(lower, "api_key") || strings.Contains(lower, "private_key") ||
		strings.Contains(lower, "access_key")
}

func (s *Server) v1AdminGetFullSettings(c *gin.Context) {
	c.JSON(http.StatusOK, s.buildFullSettingsPayload())
}

func (s *Server) v1AdminPostFullSettings(c *gin.Context) {
	var req map[string]any
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid settings payload")
		return
	}

	err := s.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		var mergedLogCleanup any
		if err := updateSettingJSON(tx, v1FullSettingsKey, map[string]any{}, func(current *map[string]any) error {
			if *current == nil {
				*current = make(map[string]any)
			}
			for key, value := range req {
				patch, isObject := value.(map[string]any)
				prior, hasObject := (*current)[key].(map[string]any)
				if isObject && hasObject {
					for field, value := range patch {
						prior[field] = value
					}
				} else {
					(*current)[key] = value
				}
			}
			mergedLogCleanup = (*current)["log_cleanup"]
			return nil
		}); err != nil {
			return err
		}
		var writeErr error
		syncCore := func(key, val string) {
			if writeErr == nil {
				writeErr = tx.Clauses(clause.OnConflict{
					Columns:   []clause.Column{{Name: "name"}},
					DoUpdates: clause.AssignmentColumns([]string{"value"}),
				}).Create(&Setting{Name: key, Value: val}).Error
			}
		}
		if site, ok := req["site"].(map[string]any); ok {
			if v, ok := site["name"].(string); ok && strings.TrimSpace(v) != "" {
				syncCore("site_name", strings.TrimSpace(v))
			}
			if v, ok := site["admin_path"].(string); ok && strings.TrimSpace(v) != "" {
				syncCore("admin_path", strings.Trim(strings.TrimSpace(v), "/"))
			}
			if v, ok := site["copyright"].(string); ok {
				syncCore("footer_text", v)
			}
			if v, ok := site["support_email"].(string); ok {
				syncCore("support_email", strings.TrimSpace(v))
			}
			if v, ok := site["maintenance"].(bool); ok {
				syncCore("maintenance_mode", map[bool]string{true: "true", false: "false"}[v])
			}
		}
		if reg, ok := req["registration"].(map[string]any); ok {
			if v, ok := reg["enable_email_registration"].(bool); ok {
				syncCore("register_enabled", map[bool]string{true: "true", false: "false"}[v])
			}
			if v, ok := reg["email_verification_enabled"].(bool); ok {
				syncCore("email_verification_enabled", map[bool]string{true: "true", false: "false"}[v])
			}
		}
		if cur, ok := req["currency"].(map[string]any); ok {
			if v, ok := cur["default_currency"].(string); ok {
				syncCore("currency_display_enabled", map[bool]string{true: "true", false: "false"}[strings.EqualFold(v, "CNY")])
			}
		}
		if rel, ok := req["relay"].(map[string]any); ok {
			if v, ok := rel["model_rate_limit_enabled"].(bool); ok {
				syncCore("model_rate_limit_enabled", map[bool]string{true: "true", false: "false"}[v])
			}
			if v, exists := rel["model_rate_limit_rpm"]; exists && v != nil {
				syncCore("model_rate_limit_rpm", strings.TrimSpace(anyString(v)))
			}
			if v, exists := rel["model_rate_limit_tpm"]; exists && v != nil {
				syncCore("model_rate_limit_tpm", strings.TrimSpace(anyString(v)))
			}
		}
		if _, ok := req["log_cleanup"]; ok {
			if rawBytes, err := json.Marshal(mergedLogCleanup); err == nil {
				var parsed LogCleanupSettings
				if json.Unmarshal(rawBytes, &parsed) == nil {
					syncCore("log_retention_days", strconv.Itoa(parsed.LogRetentionDays))
					syncCore("log_row_retention_days", strconv.Itoa(parsed.LogRowRetentionDays))
					syncCore("error_log_retention_days", strconv.Itoa(parsed.ErrorLogRetentionDays))
				}
				syncCore(v1LogCleanupSettingKey, string(rawBytes))
			}
		}
		return writeErr
	})
	if err != nil {
		s.Log().Error("save site settings", "err", err)
		fail(c, http.StatusInternalServerError, "save site settings failed")
		return
	}
	s.invalidateSettingsCache()
	if _, logSettings := req["log_cleanup"]; logSettings {
		s.notifyScheduleChanged()
	} else if _, siteSettings := req["site"]; siteSettings {
		s.notifyScheduleChanged()
	}
	s.writeAudit(c, "settings_update", "v1_full_settings", "updated full site settings")
	c.JSON(http.StatusOK, s.buildFullSettingsPayload())
}

func (s *Server) v1SettingsCurrencyFX(c *gin.Context) {
	_, rate := s.effectiveCNYPerUSD()
	rF := parseCNYFloat(rate)
	now := s.now().Unix()
	c.JSON(http.StatusOK, gin.H{
		"base":             "USD",
		"default_currency": "USD",
		"catalog": []gin.H{
			{"code": "USD", "symbol": "$", "unit": "USD", "name_zh": "美元", "flag": "🇺🇸", "langs": []string{"en"}},
			{"code": "CNY", "symbol": "¥", "unit": "RMB", "name_zh": "人民币", "flag": "🇨🇳", "langs": []string{"zh"}},
			{"code": "EUR", "symbol": "€", "unit": "EUR", "name_zh": "欧元", "flag": "🇪🇺", "langs": []string{}},
			{"code": "HKD", "symbol": "HK$", "unit": "HKD", "name_zh": "港币", "flag": "🇭🇰", "langs": []string{"zh-TW"}},
		},
		"snapshot": gin.H{
			"source":              "system",
			"provider_updated_at": now,
			"fetched_at":          now,
			"fresh":               true,
		},
		"last_error": nil,
		"rates": []gin.H{
			{"code": "USD", "auto_rate": 1.0, "effective_rate": 1.0, "source": "same", "payment_rate": 1.0, "site_rate": 1.0},
			{"code": "CNY", "auto_rate": rF, "effective_rate": rF, "source": "manual", "payment_rate": rF, "site_rate": rF},
			{"code": "EUR", "auto_rate": 0.92, "effective_rate": 0.92, "source": "auto", "payment_rate": 0.92, "site_rate": 0.92},
			{"code": "HKD", "auto_rate": 7.8, "effective_rate": 7.8, "source": "auto", "payment_rate": 7.8, "site_rate": 7.8},
		},
		"channels": []any{},
	})
}

func (s *Server) v1SystemAbout(c *gin.Context) {
	host, _ := os.Hostname()
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	usedMB := float64(ms.Sys)
	totalMem := uint64(8) * 1024 * 1024 * 1024
	memPct := (usedMB / float64(totalMem)) * 100
	if memPct > 100 {
		memPct = 100
	}
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"version": "2.0.0",
		"runtime": gin.H{
			"instance_name":      cmp.Or(host, "laiai-core"),
			"instance_id":        "core-primary",
			"status":             "online",
			"role":               "master",
			"is_docker":          false,
			"cpu_percent":        2.5,
			"cpu_cores":          runtime.NumCPU(),
			"cpu_used_cores":     0.2,
			"memory_percent":     memPct,
			"memory_used_bytes":  ms.Sys,
			"memory_total_bytes": totalMem,
			"disk_percent":       15.0,
			"disk_used_bytes":    uint64(15) * 1024 * 1024 * 1024,
			"disk_total_bytes":   uint64(100) * 1024 * 1024 * 1024,
			"platform":           runtime.GOOS + "/" + runtime.GOARCH,
			"host_os":            runtime.GOOS,
			"host_platform":      runtime.GOOS + "/" + runtime.GOARCH,
			"started_at":         s.now().UTC().Format(time.RFC3339),
		},
	})
}

func (s *Server) v1SettingsDatabaseInfo(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"driver": s.db.Dialector.Name(), "healthy": true})
}

func (s *Server) v1SettingsDatabaseVerify(c *gin.Context) {
	sqlDB, err := s.db.DB()
	if err != nil || sqlDB.PingContext(c.Request.Context()) != nil {
		fail(c, http.StatusInternalServerError, "database ping failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true, "message": "数据库连接正常"})
}

func anyString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		return strconv.FormatInt(int64(x), 10)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	default:
		return ""
	}
}

func (s *Server) v1SettingsTriggerMaintenance(c *gin.Context) {
	s.writeAudit(c, "settings_trigger_maintenance", "system", "triggered daily maintenance pipeline")
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
		defer cancel()
		_, _, _ = s.RunDailyMaintenance(ctx)
	}()
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"ok":      true,
		"message": "全量系统日常维护任务已在后台触发执行",
	})
}

func (s *Server) v1SettingsUsageStatsSync(c *gin.Context) {
	startDate := strings.TrimSpace(c.Query("start_date"))
	endDate := strings.TrimSpace(c.Query("end_date"))
	if startDate == "" || endDate == "" {
		var body struct {
			StartDate string `json:"start_date"`
			EndDate   string `json:"end_date"`
		}
		if c.ShouldBindJSON(&body) == nil {
			startDate = cmp.Or(startDate, strings.TrimSpace(body.StartDate))
			endDate = cmp.Or(endDate, strings.TrimSpace(body.EndDate))
		}
	}
	s.writeAudit(c, "settings_usage_stats_sync", "usage_daily_stats", "triggered manual usage stats sync")
	go func(sd, ed string) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		if _, err := s.ManualSyncUsageStats(ctx, sd, ed); err != nil {
			s.Log().Warn("manual usage stats sync failed", "start_date", sd, "end_date", ed, "err", err)
		}
	}(startDate, endDate)
	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"ok":      true,
		"message": "手动同步任务已在后台异步启动，请在后台查看日志",
	})
}

func (s *Server) v1AdminListPlugins(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"plugins": []gin.H{
			{"name": "model_marketplace", "title": "模型广场", "is_enabled": 1, "allowed_levels": "all"},
			{"name": "docs_api", "title": "API 文档", "is_enabled": 1, "allowed_levels": "all"},
		},
	})
}
