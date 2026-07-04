package server

// 渠道多 key 管理（商用迭代指令 §3.5；设计 §4.4 Multi-key Channel）。
// 列/增/禁/启/删 channel_credentials。明文/密文永不回显；审计只记 fingerprint + channel_id。

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/relay"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// adminListChannelKeys：GET /api/admin/channels/:id/credentials —— 列该渠道全部 key（含禁用）。
// 只回 fingerprint/status/权重/时间，绝不回明文或密文。
func (s *Server) adminListChannelKeys(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid channel id")
		return
	}
	var creds []relay.ChannelCredential
	if e := s.db.WithContext(c.Request.Context()).
		Where("channel_id = ?", id).Order("id").Find(&creds).Error; e != nil {
		fail(c, http.StatusInternalServerError, "list failed")
		return
	}
	out := make([]gin.H, 0, len(creds))
	for _, cr := range creds {
		out = append(out, gin.H{
			"id": cr.ID, "fingerprint": cr.Fingerprint, "status": cr.Status,
			"disabled_reason": cr.DisabledReason, "disabled_at": cr.DisabledAt,
			"last_used_at": cr.LastUsedAt, "mode_weight": cr.ModeWeight, "created_at": cr.CreatedAt,
		})
	}
	c.JSON(http.StatusOK, gin.H{"credentials": out})
}

type addKeyReq struct {
	APIKey     string `json:"api_key" binding:"required"`
	ModeWeight int    `json:"mode_weight"` // weighted 模式权重，默认 1
}

// adminAddChannelKey：POST /api/admin/channels/:id/credentials —— 给渠道加一把上游 key。
func (s *Server) adminAddChannelKey(c *gin.Context) {
	if s.keyring == nil {
		fail(c, http.StatusServiceUnavailable, "LAIAI_DATA_KEYS not configured")
		return
	}
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid channel id")
		return
	}
	var req addKeyReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "api_key required")
		return
	}
	var ch relay.Channel
	if e := s.db.First(&ch, id).Error; e != nil {
		fail(c, http.StatusNotFound, "channel not found")
		return
	}
	enc, err := s.keyring.Encrypt(req.APIKey)
	if err != nil {
		fail(c, http.StatusInternalServerError, "credential encrypt failed")
		return
	}
	weight := req.ModeWeight
	if weight <= 0 {
		weight = 1
	}
	cred := &relay.ChannelCredential{
		ChannelID: id, CredentialEnc: enc, Fingerprint: relay.CredentialFingerprint(req.APIKey),
		Status: relay.CredentialActive, ModeWeight: weight,
	}
	if e := s.db.Create(cred).Error; e != nil {
		fail(c, http.StatusInternalServerError, "add key failed")
		return
	}
	// 加 key 若使渠道从"全禁"恢复出 active key，顺带把 auto-disabled 渠道拉回可用。
	if ch.Status == 2 {
		s.db.Model(&relay.Channel{}).Where("id = ? AND status = 2", id).Update("status", 1)
	}
	s.writeAudit(c, "channel_key_add", strconv.FormatUint(id, 10), "added key "+cred.Fingerprint)
	s.invalidateCache(relay.KindChannel, "")
	c.JSON(http.StatusCreated, gin.H{"id": cred.ID, "fingerprint": cred.Fingerprint})
}

// adminSetChannelKeyStatus：禁用/启用一把 key（enable 参数决定方向）。
func (s *Server) adminSetChannelKeyStatus(c *gin.Context, enable bool) {
	channelID, cid, ok := parseChannelKeyIDs(c)
	if !ok {
		return
	}
	target := relay.CredentialDisabled
	updates := map[string]any{"status": relay.CredentialDisabled, "disabled_reason": "manually disabled", "disabled_at": s.now()}
	action := "channel_key_disable"
	if enable {
		target = relay.CredentialActive
		updates = map[string]any{"status": relay.CredentialActive, "disabled_reason": "", "disabled_at": nil}
		action = "channel_key_enable"
	}
	res := s.db.Model(&relay.ChannelCredential{}).
		Where("id = ? AND channel_id = ?", cid, channelID).Updates(updates)
	if res.Error != nil {
		fail(c, http.StatusInternalServerError, "update failed")
		return
	}
	if res.RowsAffected == 0 {
		fail(c, http.StatusNotFound, "key not found")
		return
	}
	// 禁用最后一把 key → 渠道无 active key，连带禁渠道（与 relay 侧 disableKey 同语义）；
	// 启用则把 auto-disabled 渠道拉回。
	if !enable {
		var active int64
		s.db.Model(&relay.ChannelCredential{}).Where("channel_id = ? AND status = ?", channelID, relay.CredentialActive).Count(&active)
		if active == 0 {
			s.db.Model(&relay.Channel{}).Where("id = ? AND status = 1", channelID).Update("status", 2)
		}
	} else {
		s.db.Model(&relay.Channel{}).Where("id = ? AND status = 2", channelID).Update("status", 1)
	}
	s.writeAudit(c, action, strconv.FormatUint(channelID, 10), "key #"+strconv.FormatUint(cid, 10)+" → status "+strconv.Itoa(int(target)))
	s.invalidateCache(relay.KindChannel, "")
	c.JSON(http.StatusOK, gin.H{"id": cid, "status": target})
}

func (s *Server) adminDisableChannelKey(c *gin.Context) { s.adminSetChannelKeyStatus(c, false) }
func (s *Server) adminEnableChannelKey(c *gin.Context)  { s.adminSetChannelKeyStatus(c, true) }

// adminDeleteChannelKey：DELETE /api/admin/channels/:id/credentials/:cid —— 硬删一把 key。
// 拒绝删最后一把 active key（渠道会无 key 可用；先加新 key 或禁渠道）。
func (s *Server) adminDeleteChannelKey(c *gin.Context) {
	channelID, cid, ok := parseChannelKeyIDs(c)
	if !ok {
		return
	}
	if e := s.db.Transaction(func(tx *gorm.DB) error {
		// 锁渠道行串行化同渠道的并发 key 变更（对抗 review：非锁读的 count-then-delete 可让
		// 两个并发删除都通过"至少剩 1 把"检查、把最后两把都删空）。SQLite 单连接天然串行。
		lockTx := tx
		if tx.Dialector.Name() == "mysql" {
			lockTx = tx.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		var ch relay.Channel
		if err := lockTx.First(&ch, channelID).Error; err != nil {
			return errNotFound
		}
		var cred relay.ChannelCredential
		if err := tx.Where("id = ? AND channel_id = ?", cid, channelID).First(&cred).Error; err != nil {
			return errNotFound
		}
		if cred.Status == relay.CredentialActive {
			var active int64
			tx.Model(&relay.ChannelCredential{}).Where("channel_id = ? AND status = ?", channelID, relay.CredentialActive).Count(&active)
			if active <= 1 {
				return errLastKey
			}
		}
		return tx.Delete(&relay.ChannelCredential{}, cid).Error
	}); e != nil {
		switch e {
		case errNotFound:
			fail(c, http.StatusNotFound, "key not found")
		case errLastKey:
			fail(c, http.StatusBadRequest, "cannot delete the last active key — add another key or disable the channel first")
		default:
			fail(c, http.StatusInternalServerError, "delete failed")
		}
		return
	}
	s.writeAudit(c, "channel_key_delete", strconv.FormatUint(channelID, 10), "deleted key #"+strconv.FormatUint(cid, 10))
	s.invalidateCache(relay.KindChannel, "")
	c.JSON(http.StatusOK, gin.H{"deleted": cid})
}

// parseChannelKeyIDs：解析 :id 与 :cid，非法即写 400 并返回 ok=false。
func parseChannelKeyIDs(c *gin.Context) (channelID, cid uint64, ok bool) {
	var err error
	if channelID, err = strconv.ParseUint(c.Param("id"), 10, 64); err != nil {
		fail(c, http.StatusBadRequest, "invalid channel id")
		return 0, 0, false
	}
	if cid, err = strconv.ParseUint(strings.TrimSpace(c.Param("cid")), 10, 64); err != nil {
		fail(c, http.StatusBadRequest, "invalid credential id")
		return 0, 0, false
	}
	return channelID, cid, true
}

var (
	errNotFound = gorm.ErrRecordNotFound
	errLastKey  = errSentinel("last active key")
)

type errSentinel string

func (e errSentinel) Error() string { return string(e) }
