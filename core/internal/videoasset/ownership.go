package videoasset

import (
	"context"
	"errors"
	"fmt"
	"github.com/laiai/laiai-next/core/internal/identity"

	"gorm.io/gorm"
)

// Ownership is immutable resource identity. A zero owner is historical evidence
// that could not be attributed; it is never assigned to the next reader.
type Ownership struct {
	OrgID        uint64 `gorm:"primaryKey"`
	ResourceType string `gorm:"primaryKey"`
	ResourceID   string `gorm:"primaryKey"`
	OwnerUserID  uint64
	OwnerKeyID   uint64
	ManageAll    bool
}

func (Ownership) TableName() string { return "video_asset_owners" }

type callerContextKey struct{}

// WithCaller accepts a server-authenticated identity only. It deliberately
// records invalid callers too, so an absent identity cannot become worker access.
func WithCaller(ctx context.Context, c CreateContext) context.Context {
	return context.WithValue(ctx, callerContextKey{}, c)
}
func WithAPIKey(ctx context.Context, key *identity.APIKey) context.Context {
	c := CreateContext{CallerKind: "api_key"}
	if key != nil {
		c.OrgID, c.KeyID = key.OrgID, key.ID
		if key.UserID != nil {
			c.UserID = *key.UserID
		}
	}
	return WithCaller(ctx, c)
}
func callerFrom(ctx context.Context) (CreateContext, bool) {
	c, ok := ctx.Value(callerContextKey{}).(CreateContext)
	return c, ok
}
func validCaller(c CreateContext, org uint64) bool {
	return org > 0 && c.OrgID == org && (c.UserID > 0 || c.KeyID > 0) && (!c.Superadmin || c.CallerKind == "session_user" && c.UserID > 0 && c.KeyID == 0)
}
func ownerIdentity(c CreateContext) (uint64, uint64) {
	if c.UserID > 0 {
		return c.UserID, 0
	}
	return 0, c.KeyID
}
func actorCursor(ctx context.Context, kind string) string {
	c, ok := callerFrom(ctx)
	if !ok {
		return kind
	} // Internal callers retain the historical cursor namespace.
	u, k := ownerIdentity(c)
	return fmt.Sprintf("%s:actor:%d:%d:%d:%t", kind, c.OrgID, u, k, c.Superadmin)
}

// owned is the repository predicate shared by all public/session entry points.
// Org-only functions are also used internally by workers; HTTP adapters MUST
// always install WithCaller before invoking them, even for invalid identities.
func owned(ctx context.Context, q *gorm.DB, org uint64, kind, column string) *gorm.DB {
	c, ok := callerFrom(ctx)
	if !ok {
		return q
	}
	if !validCaller(c, org) {
		return q.Where("1 = 0")
	}
	if c.Superadmin {
		return q
	}
	u, k := ownerIdentity(c)
	return q.Where("EXISTS (SELECT 1 FROM video_asset_owners vao WHERE vao.org_id = ? AND vao.resource_type = ? AND vao.resource_id = "+column+" AND vao.owner_user_id = ? AND vao.owner_key_id = ?)", org, kind, u, k)
}

func registerOwnerTx(tx *gorm.DB, c CreateContext, kind, id string) error {
	if !validCaller(c, c.OrgID) || !validID(id) {
		return ErrInvalid
	}
	u, k := ownerIdentity(c)
	row := Ownership{OrgID: c.OrgID, ResourceType: kind, ResourceID: id, OwnerUserID: u, OwnerKeyID: k, ManageAll: kind == "sync" && c.Superadmin}
	if err := tx.Create(&row).Error; err != nil {
		return ErrStorage
	}
	return nil
}
func inheritOwnerTx(tx *gorm.DB, org uint64, parentKind, parentID, kind, id string) error {
	var row Ownership
	err := tx.Where("org_id = ? AND resource_type = ? AND resource_id = ?", org, parentKind, parentID).Take(&row).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return ErrStorage
	}
	if errors.Is(err, gorm.ErrRecordNotFound) {
		row = Ownership{OrgID: org}
	}
	row.ResourceType, row.ResourceID, row.ManageAll = kind, id, false
	if tx.Create(&row).Error != nil {
		return ErrStorage
	}
	return nil
}
func resourceVisible(ctx context.Context, tx *gorm.DB, org uint64, kind, id string) error {
	c, present := callerFrom(ctx)
	if !present {
		return nil
	}
	if !validCaller(c, org) {
		return ErrNotFound
	}
	if c.Superadmin {
		return nil
	}
	u, k := ownerIdentity(c)
	var n int64
	if tx.Model(&Ownership{}).Where("org_id = ? AND resource_type = ? AND resource_id = ? AND owner_user_id = ? AND owner_key_id = ?", org, kind, id, u, k).Count(&n).Error != nil {
		return ErrStorage
	}
	if n != 1 {
		return ErrNotFound
	}
	return nil
}

func (s *Service) syncOwnerContext(ctx context.Context, job SyncJob) (context.Context, error) {
	var row Ownership
	err := s.db.WithContext(ctx).Where("org_id = ? AND resource_type = ? AND resource_id = ?", job.OrgID, "sync", job.ID).Take(&row).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, ErrStorage
	}
	c := CreateContext{OrgID: job.OrgID, UserID: row.OwnerUserID, KeyID: row.OwnerKeyID, CallerKind: "api_key"}
	if row.ManageAll && row.OwnerUserID > 0 {
		c.CallerKind, c.Superadmin = "session_user", true
	}
	return WithCaller(ctx, c), nil
}
