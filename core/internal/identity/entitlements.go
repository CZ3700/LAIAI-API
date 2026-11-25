package identity

import (
	"errors"

	"gorm.io/gorm"
)

// Entitlements captures organization feature gates and resource caps stored on organizations.
type Entitlements struct {
	OrgID         uint64 `gorm:"column:id;primaryKey"`
	FeatureImages bool   `gorm:"column:feature_images"`
	FeatureVideo  bool   `gorm:"column:feature_video"`
	MaxMembers    int    `gorm:"column:max_members"`
	MaxAPIKeys    int    `gorm:"column:max_api_keys"`
}

func (Entitlements) TableName() string { return "organizations" }

var (
	ErrMemberLimit = errors.New("identity: organization member limit reached")
	ErrKeyLimit    = errors.New("identity: organization api key limit reached")
)

func DefaultEntitlements(orgID uint64) Entitlements {
	return Entitlements{OrgID: orgID, FeatureImages: true, FeatureVideo: true, MaxMembers: 0, MaxAPIKeys: 0}
}

func LoadEntitlements(db *gorm.DB, orgID uint64) (Entitlements, error) {
	var e Entitlements
	err := db.Select("id, feature_images, feature_video, max_members, max_api_keys").
		Where("id = ?", orgID).First(&e).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return DefaultEntitlements(orgID), nil
	}
	if err != nil {
		return Entitlements{}, err
	}
	return e, nil
}

func UpsertEntitlements(db *gorm.DB, e Entitlements) error {
	return db.Model(&Organization{}).Where("id = ?", e.OrgID).Updates(map[string]any{
		"feature_images": e.FeatureImages,
		"feature_video":  e.FeatureVideo,
		"max_members":    e.MaxMembers,
		"max_api_keys":   e.MaxAPIKeys,
	}).Error
}
