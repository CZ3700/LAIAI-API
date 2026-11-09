package video

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/routinggroup"
	"github.com/laiai/laiai-next/core/internal/videoasset"
	"gorm.io/gorm"
)

// GroupAssetProject selects the Ark project for new asset operations made
// through a grouped key or the organization's current default routing group.
// Historical assets retain their own immutable scope identity.
type GroupAssetProject struct {
	GroupID         uint64    `gorm:"primaryKey" json:"group_id"`
	ProviderScopeID uint64    `json:"provider_scope_id"`
	ScopeVersion    uint64    `json:"scope_version"`
	Revision        uint64    `json:"revision"`
	UpdatedBy       uint64    `json:"-"`
	UpdatedAt       time.Time `json:"updated_at"`
}

func (GroupAssetProject) TableName() string { return "video_group_asset_projects" }

type GroupAssetProjectInput struct {
	GroupID          uint64 `json:"group_id"`
	ProviderScopeID  uint64 `json:"provider_scope_id"`
	ScopeVersion     uint64 `json:"scope_version"`
	ExpectedRevision uint64 `json:"expected_revision"`
}

func groupAssetPublicationReadyTx(tx *gorm.DB, group routinggroup.Group, scopeID uint64) error {
	var publications []struct {
		PublicModel     string
		ProviderScopeID uint64
		ChannelStatus   int8
		ChannelOwner    uint64
		VerifiedAt      *time.Time
	}
	if err := tx.Table("channel_group_bindings AS b").
		Select("m.public_model, m.provider_scope_id, c.status AS channel_status, c.owner_org_id AS channel_owner, m.verified_at").
		Joins("JOIN channels c ON c.id = b.channel_id").
		Joins("JOIN video_model_configs m ON m.channel_id = c.id AND m.enabled = 1").
		Where("b.group_id = ?", group.ID).Find(&publications).Error; err != nil {
		return ErrUnavailable
	}
	matched := false
	for _, p := range publications {
		if !ark.IsPublicModel(p.PublicModel) {
			continue
		}
		if p.ChannelStatus != 1 || p.ChannelOwner != group.OwnerOrgID || p.VerifiedAt == nil || p.ProviderScopeID != scopeID {
			return ErrCapability
		}
		matched = true
	}
	if !matched {
		return ErrCapability
	}
	return nil
}

// SetGroupAssetProject is an administrator-owned association, not an upstream
// project creation. Only platform groups may use platform Ark credentials.
func (s *Service) SetGroupAssetProject(ctx context.Context, actor *identity.User, in GroupAssetProjectInput) (GroupAssetProject, error) {
	var out GroupAssetProject
	if err := assetAdmin(actor); err != nil {
		return out, err
	}
	if s == nil || s.DB == nil || s.Now == nil || in.GroupID == 0 || in.ProviderScopeID == 0 || in.ScopeVersion == 0 {
		return out, ErrInvalid
	}
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var group routinggroup.Group
		if err := locked(tx).Where("id = ?", in.GroupID).Take(&group).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return ErrNotFound
			}
			return ErrUnavailable
		}
		if group.Status != routinggroup.Enabled || group.OwnerOrgID != 0 {
			return ErrForbidden
		}
		if err := groupAssetPublicationReadyTx(tx, group, in.ProviderScopeID); err != nil {
			return err
		}
		var provider ProviderScope
		if err := locked(tx).Where("id = ?", in.ProviderScopeID).Take(&provider).Error; err != nil || !validAssetProvider(provider) {
			return ErrCapability
		}
		var config AssetScopeConfig
		if err := locked(tx).Where("provider_scope_id = ? AND version = ?", in.ProviderScopeID, in.ScopeVersion).Take(&config).Error; err != nil || !config.Enabled || config.CredentialCipher == "" || config.AccountRef != provider.AccountRef || config.Region != provider.Region || config.ProjectName != provider.ProjectName {
			return ErrCapability
		}
		var caps videoasset.Capabilities
		if json.Unmarshal([]byte(config.CapabilitiesJSON), &caps) != nil || (!caps.VirtualGroups && !caps.Portrait) {
			return ErrCapability
		}
		var current GroupAssetProject
		err := locked(tx).Where("group_id = ?", in.GroupID).Take(&current).Error
		now := s.Now().UTC()
		switch {
		case errors.Is(err, gorm.ErrRecordNotFound):
			if in.ExpectedRevision != 0 {
				return ErrConflict
			}
			out = GroupAssetProject{GroupID: in.GroupID, ProviderScopeID: in.ProviderScopeID, ScopeVersion: in.ScopeVersion, Revision: 1, UpdatedBy: actor.ID, UpdatedAt: now}
			if err := tx.Create(&out).Error; err != nil {
				return ErrUnavailable
			}
		case err != nil:
			return ErrUnavailable
		default:
			if in.ExpectedRevision != current.Revision {
				return ErrConflict
			}
			if current.ProviderScopeID == in.ProviderScopeID && current.ScopeVersion == in.ScopeVersion {
				out = current
				return nil
			}
			out = current
			out.ProviderScopeID, out.ScopeVersion, out.Revision, out.UpdatedBy, out.UpdatedAt = in.ProviderScopeID, in.ScopeVersion, current.Revision+1, actor.ID, now
			res := tx.Model(&GroupAssetProject{}).Where("group_id = ? AND revision = ?", in.GroupID, current.Revision).Updates(map[string]any{"provider_scope_id": out.ProviderScopeID, "scope_version": out.ScopeVersion, "revision": out.Revision, "updated_by": out.UpdatedBy, "updated_at": out.UpdatedAt})
			if res.Error != nil || res.RowsAffected != 1 {
				return ErrConflict
			}
		}
		return s.assetAuditTx(tx, 0, "routing_group_asset_project", strconv.FormatUint(in.GroupID, 10), "project_associated", "", strconv.FormatUint(actor.ID, 10))
	})
	if err != nil {
		return GroupAssetProject{}, err
	}
	return out, nil
}

func (s *Service) ReadGroupAssetProject(ctx context.Context, actor *identity.User, groupID uint64) (GroupAssetProject, error) {
	var out GroupAssetProject
	if err := assetAdmin(actor); err != nil {
		return out, err
	}
	if s == nil || s.DB == nil || groupID == 0 {
		return out, ErrInvalid
	}
	err := s.DB.WithContext(ctx).Where("group_id = ?", groupID).Take(&out).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return GroupAssetProject{}, ErrNotFound
	}
	if err != nil {
		return GroupAssetProject{}, ErrUnavailable
	}
	return out, nil
}

// callerAssetGroupRouteTx resolves only new work. A grouped key cannot fall
// through to the organization or global project when its group is unconfigured.
func (s *Service) callerAssetGroupRouteTx(ctx context.Context, tx *gorm.DB, org uint64) (*AssetOrgRoute, error) {
	keyID, session, known := videoasset.CallerRouteIdentity(ctx, org)
	if !known {
		return nil, nil // internal worker or historical operation
	}
	var groupID uint64
	if keyID != 0 {
		var key struct {
			Mode    string  `gorm:"column:model_access_mode"`
			GroupID *uint64 `gorm:"column:routing_group_id"`
		}
		if err := tx.Table("api_keys").Select("model_access_mode, routing_group_id").Where("id = ? AND org_id = ? AND status = 1", keyID, org).Take(&key).Error; err != nil {
			return nil, ErrForbidden
		}
		if key.Mode == identity.ModelAccessLegacy {
			return nil, nil
		}
		if key.Mode != "inherit_group" || key.GroupID == nil || *key.GroupID == 0 {
			return nil, ErrForbidden
		}
		groupID = *key.GroupID
	} else if session {
		var grant routinggroup.Grant
		err := tx.Where("org_id = ? AND default_slot = 1", org).Take(&grant).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		if err != nil {
			return nil, ErrUnavailable
		}
		groupID = grant.GroupID
	}
	if groupID == 0 {
		return nil, nil
	}
	var group routinggroup.Group
	var grant routinggroup.Grant
	if err := locked(tx).Where("id = ? AND status = ?", groupID, routinggroup.Enabled).Take(&group).Error; err != nil || group.OwnerOrgID != 0 && group.OwnerOrgID != org {
		return nil, ErrForbidden
	}
	if err := locked(tx).Where("org_id = ? AND group_id = ? AND status = ?", org, groupID, routinggroup.Enabled).Take(&grant).Error; err != nil {
		return nil, ErrForbidden
	}
	var mapping GroupAssetProject
	if err := locked(tx).Where("group_id = ?", groupID).Take(&mapping).Error; err != nil {
		return nil, ErrCapability
	}
	if err := groupAssetPublicationReadyTx(tx, group, mapping.ProviderScopeID); err != nil {
		return nil, err
	}
	return &AssetOrgRoute{OrgID: org, ProviderScopeID: mapping.ProviderScopeID, ScopeVersion: mapping.ScopeVersion, Enabled: true, DefaultRoute: true}, nil
}
