package video

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/videoasset"
	"gorm.io/gorm"
)

// The singleton is opt-in and never contains credentials or customer resources.
type AssetSharedDefault struct {
	ID              uint64    `gorm:"primaryKey" json:"-"`
	Revision        uint64    `json:"revision"`
	ProviderScopeID uint64    `json:"provider_scope_id"`
	ScopeVersion    uint64    `json:"scope_version"`
	Enabled         bool      `json:"enabled"`
	AllowAsync      bool      `json:"allow_async"`
	UpdatedBy       uint64    `json:"-"`
	UpdatedAt       time.Time `json:"-"`
}

func (AssetSharedDefault) TableName() string { return "video_asset_shared_defaults" }

type AssetSharedDefaultInput struct {
	ExpectedRevision uint64 `json:"expected_revision"`
	ProviderScopeID  uint64 `json:"provider_scope_id"`
	ScopeVersion     uint64 `json:"scope_version"`
	Enabled          bool   `json:"enabled"`
	AllowAsync       bool   `json:"allow_async"`
}

func (s *Service) SetAssetSharedDefault(ctx context.Context, actor *identity.User, in AssetSharedDefaultInput) error {
	if err := assetAdmin(actor); err != nil {
		return err
	}
	if in.ProviderScopeID == 0 || in.ScopeVersion == 0 || in.Enabled && !in.AllowAsync {
		return ErrInvalid
	}
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var row AssetSharedDefault
		if err := locked(tx).Where("id = 1").Take(&row).Error; err != nil {
			return ErrUnavailable
		}
		if row.Revision != in.ExpectedRevision {
			return ErrConflict
		}
		var cfg AssetScopeConfig
		if err := tx.Where("provider_scope_id = ? AND version = ?", in.ProviderScopeID, in.ScopeVersion).Take(&cfg).Error; err != nil {
			return ErrCapability
		}
		if in.Enabled {
			var p ProviderScope
			if tx.First(&p, in.ProviderScopeID).Error != nil || !validAssetProvider(p) || !cfg.Enabled || cfg.CredentialCipher == "" || cfg.AccountRef != p.AccountRef || cfg.Region != p.Region || cfg.ProjectName != p.ProjectName {
				return ErrCapability
			}
			var caps videoasset.Capabilities
			if json.Unmarshal([]byte(cfg.CapabilitiesJSON), &caps) != nil || (!caps.VirtualGroups && !caps.Portrait) {
				return ErrCapability
			}
		}
		result := tx.Model(&AssetSharedDefault{}).Where("id = 1 AND revision = ?", in.ExpectedRevision).Updates(map[string]any{"revision": in.ExpectedRevision + 1, "provider_scope_id": in.ProviderScopeID, "scope_version": in.ScopeVersion, "enabled": in.Enabled, "allow_async": in.Enabled && in.AllowAsync, "updated_by": actor.ID, "updated_at": s.Now().UTC()})
		if result.Error != nil {
			return ErrUnavailable
		}
		if result.RowsAffected != 1 {
			return ErrConflict
		}
		return s.assetAuditTx(tx, 0, "asset_shared_default", "1", "shared_default_published", "", strconv.FormatUint(actor.ID, 10))
	})
}

// Any explicit organization routing (including a disabled route) is an override.
// A global default cannot silently recover a disabled or stale tenant binding.
func (s *Service) assetRouteTx(tx *gorm.DB, org, providerID uint64) (AssetOrgRoute, AssetSharedDefault, error) {
	var explicit []AssetOrgRoute
	var shared AssetSharedDefault
	if err := tx.Where("org_id = ?", org).Find(&explicit).Error; err != nil {
		return AssetOrgRoute{}, shared, ErrUnavailable
	}
	if len(explicit) > 0 {
		var selected []AssetOrgRoute
		for _, row := range explicit {
			if row.Enabled && ((providerID != 0 && row.ProviderScopeID == providerID) || (providerID == 0 && row.DefaultRoute)) {
				selected = append(selected, row)
			}
		}
		if len(selected) != 1 {
			return AssetOrgRoute{}, shared, ErrCapability
		}
		return selected[0], shared, nil
	}
	if err := tx.Where("id = 1").Take(&shared).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return AssetOrgRoute{}, shared, ErrCapability
		}
		return AssetOrgRoute{}, shared, ErrUnavailable
	}
	if !shared.Enabled || !shared.AllowAsync || shared.ProviderScopeID == 0 || shared.ScopeVersion == 0 || (providerID != 0 && shared.ProviderScopeID != providerID) {
		return AssetOrgRoute{}, shared, ErrCapability
	}
	return AssetOrgRoute{OrgID: org, ProviderScopeID: shared.ProviderScopeID, ScopeVersion: shared.ScopeVersion, Enabled: true, DefaultRoute: true}, shared, nil
}

func assetExecutionPolicyTx(tx *gorm.DB, org uint64, inherited AssetSharedDefault) (Policy, error) {
	var policy Policy
	if err := tx.Where("org_id = ?", org).Take(&policy).Error; err != nil {
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return policy, ErrUnavailable
		}
		if inherited.Enabled && inherited.AllowAsync {
			// This is an asset-only runtime view, never a video_org_policies write.
			return Policy{OrgID: org, AsyncExecutionAllowed: true, CaptureHistory: false, RetentionDays: 1}, nil
		}
		return policy, errors.Join(ErrForbidden, videoasset.ErrExecutionPolicy)
	}
	if policy.RetentionDays < 1 || policy.RetentionDays > 30 {
		return policy, errors.Join(ErrForbidden, videoasset.ErrExecutionPolicy)
	}
	return policy, nil
}
