package server

import (
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"
)

// —— 运营设置（0013，运行时可配项） ——

type Setting struct {
	Name  string `gorm:"primaryKey;column:name" json:"name"`
	Value string `gorm:"column:value" json:"value"`
}

func (Setting) TableName() string { return "ext_settings" }

// 已知设置键（公开/admin 共用），含默认值。
var settingDefaults = map[string]string{
	"site_name":                   "LAIAI",
	"register_enabled":            "true",
	"email_verification_enabled":  "false",
	"maintenance_mode":            "false",
	"announcement":                "",
	"terms_of_service":            "",
	"privacy_policy":              "",
}

func (s *Server) getSetting(name string) string {
	var row Setting
	if err := s.db.Where("name = ?", name).First(&row).Error; err == nil {
		return row.Value
	}
	return settingDefaults[name]
}

func (s *Server) getBoolSetting(name string) bool {
	return s.getSetting(name) == "true"
}

// adminGetSettings：GET /api/admin/settings —— 全部已知设置（落库值优先，否则默认）。
func (s *Server) adminGetSettings(c *gin.Context) {
	var rows []Setting
	s.db.Find(&rows)
	stored := make(map[string]string, len(rows))
	for _, r := range rows {
		stored[r.Name] = r.Value
	}
	out := make(map[string]string, len(settingDefaults))
	for k, def := range settingDefaults {
		if v, ok := stored[k]; ok {
			out[k] = v
		} else {
			out[k] = def
		}
	}
	c.JSON(http.StatusOK, gin.H{"settings": out})
}

// adminSetSettings：PUT /api/admin/settings {name: value, ...} —— 只接受已知键。
func (s *Server) adminSetSettings(c *gin.Context) {
	var req map[string]string
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid request body")
		return
	}
	for name, value := range req {
		if _, known := settingDefaults[name]; !known {
			fail(c, http.StatusBadRequest, "unknown setting: "+name)
			return
		}
		if err := s.db.Save(&Setting{Name: name, Value: value}).Error; err != nil {
			fail(c, http.StatusInternalServerError, "save failed")
			return
		}
	}
	s.writeAudit(c, "settings_update", "", "updated platform settings")
	c.JSON(http.StatusOK, gin.H{"updated": len(req)})
}

// getConfig：GET /api/config —— 公开运营配置（无需登录；前端门面/注册页用）。
func (s *Server) getConfig(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"site_name":                  s.getSetting("site_name"),
		"register_enabled":           s.getBoolSetting("register_enabled"),
		"email_verification_enabled": s.getBoolSetting("email_verification_enabled"),
		"maintenance":                s.getBoolSetting("maintenance_mode"),
		"announcement":               s.getSetting("announcement"),
		"terms_of_service":           s.getSetting("terms_of_service"),
		"privacy_policy":             s.getSetting("privacy_policy"),
	})
}

// —— 审计日志（0013，admin 操作留痕） ——

type AuditLog struct {
	ID        uint64 `gorm:"primaryKey" json:"id"`
	Actor     string `gorm:"column:actor" json:"actor"`
	Action    string `gorm:"column:action" json:"action"`
	Target    string `gorm:"column:target" json:"target"`
	Detail    string `gorm:"column:detail" json:"detail"`
	IP        string `gorm:"column:ip" json:"ip"`
	CreatedAt int64  `gorm:"column:created_at" json:"created_at"`
}

func (AuditLog) TableName() string { return "audit_logs" }

// writeAudit 记录一条 admin 操作（尽力而为，失败只忽略不阻断业务）。
func (s *Server) writeAudit(c *gin.Context, action, target, detail string) {
	_ = s.db.Create(&AuditLog{
		Actor:     "admin", // admin-token 操作；未来 root 用户记 user_id
		Action:    action,
		Target:    target,
		Detail:    detail,
		IP:        c.ClientIP(),
		CreatedAt: s.now().UnixMilli(),
	}).Error
}

// adminListAuditLogs：GET /api/admin/audit-logs?page=&action= —— 分页倒序。
func (s *Server) adminListAuditLogs(c *gin.Context) {
	page := 1
	if n, err := strconv.Atoi(c.Query("page")); err == nil && n > 0 {
		page = n
	}
	const pageSize = 30
	q := s.db.Model(&AuditLog{})
	if a := c.Query("action"); a != "" {
		q = q.Where("action = ?", a)
	}
	var total int64
	q.Count(&total)
	var rows []AuditLog
	q.Order("id DESC").Limit(pageSize).Offset((page - 1) * pageSize).Find(&rows)
	c.JSON(http.StatusOK, gin.H{"total": total, "page": page, "page_size": pageSize, "items": rows})
}
