package videoasset

import (
	"context"
	"crypto/hmac"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"net/url"
	"strings"
	"time"
)

const PortraitCallbackPath = "/v1/video/portrait-callback"

func validPlatformCallback(raw string) bool {
	if !validHTTPS(raw, 2048) {
		return false
	}
	u, err := url.Parse(raw)
	return err == nil && u.Path == PortraitCallbackPath && u.RawQuery == "" && u.Fragment == ""
}

type callbackClaims struct {
	OrgID                        uint64
	SessionID, TargetID, Version string
	Expires                      int64
}

func (s *Service) callbackURL(ctx context.Context, op Operation) (string, error) {
	if !validPlatformCallback(s.options.PlatformCallbackURL) {
		return "", ErrUnsupported
	}
	var token string
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := lockTenant(tx, op.OrgID); err != nil {
			return err
		}
		var p Portrait
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("org_id = ? AND session_id = ?", op.OrgID, op.ResourceID).Take(&p).Error; err != nil {
			return ErrNotFound
		}
		if p.SessionStatus != "creating" || p.CallbackTargetID == "" || len(s.options.HMACKeys[op.HMACKeyVersion]) == 0 {
			return ErrConflict
		}
		claims := callbackClaims{OrgID: op.OrgID, SessionID: p.ID, TargetID: p.CallbackTargetID, Version: op.HMACKeyVersion, Expires: op.UpdatedAt.Add(30 * time.Minute).Unix()}
		payload := base64.RawURLEncoding.EncodeToString(bodyJSON(claims))
		signature := hex.EncodeToString(mac(s.options.HMACKeys[claims.Version], "portrait-callback-v1:"+payload))
		token = payload + "." + signature
		digest := s.digest(claims.Version, "callback-state", op.OrgID, p.ID, token)
		if err := tx.Model(&Portrait{}).Where("org_id = ? AND session_id = ?", op.OrgID, p.ID).Updates(map[string]any{"callback_state_hash": digest, "callback_state_version": claims.Version}).Error; err != nil {
			return ErrStorage
		}
		return s.event(tx, op.OrgID, "portrait", p.ID, op.ID, "callback_bound", "", op.LeaseOwner)
	})
	if err != nil {
		return "", err
	}
	u, _ := url.Parse(s.options.PlatformCallbackURL)
	q := u.Query()
	q.Set("state", token)
	u.RawQuery = q.Encode()
	return u.String(), nil
}
func (s *Service) parseCallbackState(raw string) (callbackClaims, error) {
	if len(raw) > 2048 {
		return callbackClaims{}, ErrInvalid
	}
	parts := strings.Split(raw, ".")
	if len(parts) != 2 {
		return callbackClaims{}, ErrInvalid
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return callbackClaims{}, ErrInvalid
	}
	var claims callbackClaims
	if json.Unmarshal(body, &claims) != nil || claims.OrgID == 0 || !validID(claims.SessionID) || !validID(claims.TargetID) || len(s.options.HMACKeys[claims.Version]) == 0 {
		return callbackClaims{}, ErrInvalid
	}
	signature, err := hex.DecodeString(parts[1])
	expected := mac(s.options.HMACKeys[claims.Version], "portrait-callback-v1:"+parts[0])
	if err != nil || !hmac.Equal(signature, expected) || claims.Expires <= s.now().Unix() {
		return callbackClaims{}, ErrInvalid
	}
	return claims, nil
}

// HandlePortraitCallback must be mounted at PortraitCallbackPath OUTSIDE the
// Gateway Key middleware. Signed state permits one redirect/verification hint,
// never authentication success or a business license. Ignore provider/client
// resultCode, GroupId and arbitrary redirect parameters completely.
func (s *Service) HandlePortraitCallback(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Header("Referrer-Policy", "no-referrer")
	raw := c.Query("state")
	c.Request.URL.RawQuery = ""
	claims, err := s.parseCallbackState(raw)
	if err != nil || s.options.ResolveCallbackByID == nil {
		c.AbortWithStatus(400)
		return
	}
	target, err := s.options.ResolveCallbackByID(c.Request.Context(), claims.OrgID, claims.TargetID)
	if err != nil || target.ID != claims.TargetID || !validHTTPS(target.URL, 2048) {
		c.AbortWithStatus(400)
		return
	}
	err = s.db.WithContext(c.Request.Context()).Transaction(func(tx *gorm.DB) error {
		if err := lockTenant(tx, claims.OrgID); err != nil {
			return err
		}
		var p Portrait
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("org_id = ? AND session_id = ?", claims.OrgID, claims.SessionID).Take(&p).Error; err != nil {
			return ErrNotFound
		}
		if p.CallbackTargetID != claims.TargetID || p.CallbackStateVersion != claims.Version || p.CallbackUsedAt != nil || (p.SessionStatus != "pending" && p.SessionStatus != "succeeded") {
			return ErrConflict
		}
		expected := s.digest(claims.Version, "callback-state", claims.OrgID, p.ID, raw)
		if !hmac.Equal([]byte(expected), []byte(p.CallbackStateHash)) {
			return ErrInvalid
		}
		updated := tx.Model(&Portrait{}).Where("org_id = ? AND session_id = ? AND callback_used_at IS NULL", claims.OrgID, p.ID).Update("callback_used_at", nowMillis(s))
		if updated.Error != nil {
			return ErrStorage
		}
		if updated.RowsAffected != 1 {
			return ErrConflict
		}
		return s.event(tx, claims.OrgID, "portrait", p.ID, p.CreateOperationID, "callback_consumed", "", "callback_browser")
	})
	if err != nil {
		c.AbortWithStatus(400)
		return
	}
	// Scheduled GetVisualValidateResult remains the only identity confirmation.
	c.Redirect(303, target.URL)
}
