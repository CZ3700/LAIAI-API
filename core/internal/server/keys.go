package server

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/identity"
	"gorm.io/gorm"
)

type createKeyReq struct {
	Name          string `json:"name"`
	Scopes        string `json:"scopes"`
	ExpiresInDays int    `json:"expires_in_days"` // 0 = 永不过期
}

func (s *Server) createKey(c *gin.Context) {
	claims := mustClaims(c)
	var req createKeyReq
	if err := c.ShouldBindJSON(&req); err != nil {
		fail(c, http.StatusBadRequest, "invalid request body")
		return
	}
	var expires *time.Time
	if req.ExpiresInDays > 0 {
		t := s.now().AddDate(0, 0, req.ExpiresInDays)
		expires = &t
	}
	var plaintext string
	var key *identity.APIKey
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var err error
		uid := claims.UserID
		plaintext, key, err = identity.IssueKey(tx, identity.IssueKeyInput{
			OrgID: claims.OrgID, UserID: &uid,
			Name: req.Name, Scopes: req.Scopes, ExpiresAt: expires,
		})
		return err
	})
	if err != nil {
		fail(c, http.StatusInternalServerError, "key issue failed")
		return
	}
	// 明文仅此一次返回（红线 8）
	c.JSON(http.StatusCreated, gin.H{
		"id": key.ID, "key": plaintext, "prefix": key.Prefix, "name": key.Name,
	})
}

func (s *Server) listKeys(c *gin.Context) {
	claims := mustClaims(c)
	var keys []identity.APIKey
	// org_id 谓词强制（红线 5：org 隔离在 repository 层）
	if err := s.db.Where("org_id = ?", claims.OrgID).Order("id DESC").Find(&keys).Error; err != nil {
		fail(c, http.StatusInternalServerError, "list failed")
		return
	}
	out := make([]gin.H, 0, len(keys))
	for _, k := range keys {
		out = append(out, gin.H{
			"id": k.ID, "prefix": k.Prefix, "name": k.Name,
			"scopes": k.Scopes, "status": k.Status, "expires_at": k.ExpiresAt,
		})
	}
	c.JSON(http.StatusOK, gin.H{"keys": out})
}

func (s *Server) revokeKey(c *gin.Context) {
	claims := mustClaims(c)
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid key id")
		return
	}
	// 先校验归属（org 隔离），再吊销
	k := &identity.APIKey{}
	if err := s.db.Where("id = ? AND org_id = ?", id, claims.OrgID).First(k).Error; err != nil {
		fail(c, http.StatusNotFound, "key not found")
		return
	}
	if err := s.db.Transaction(func(tx *gorm.DB) error {
		return identity.RevokeKey(tx, k.ID)
	}); err != nil {
		fail(c, http.StatusInternalServerError, "revoke failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"revoked": k.ID})
}

// deleteKey：DELETE /api/keys/:id/purge —— 物理删除（仅限已吊销的 key）。
// 活跃 key 必须先吊销再删，避免误删导致在用凭证瞬间失效（吊销是软态可审计）。
func (s *Server) deleteKey(c *gin.Context) {
	claims := mustClaims(c)
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil {
		fail(c, http.StatusBadRequest, "invalid key id")
		return
	}
	k := &identity.APIKey{}
	if err := s.db.Where("id = ? AND org_id = ?", id, claims.OrgID).First(k).Error; err != nil {
		fail(c, http.StatusNotFound, "key not found")
		return
	}
	if k.Status == 1 {
		fail(c, http.StatusBadRequest, "revoke the key before deleting it")
		return
	}
	if err := s.db.Where("id = ? AND org_id = ?", id, claims.OrgID).Delete(&identity.APIKey{}).Error; err != nil {
		fail(c, http.StatusInternalServerError, "delete failed")
		return
	}
	c.JSON(http.StatusOK, gin.H{"deleted": k.ID})
}
