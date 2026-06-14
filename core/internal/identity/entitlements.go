package identity

import (
	"errors"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Org Entitlements（P0-2 功能调配）：超级管理员按 org 分配可用功能与资源上限。
// 设计要点——无配置行 = 平台默认（全功能开启、不限量），保证既有 org 零影响；
// 超管显式收紧才落行。读路径是热点（relay/task 每请求查一次），保持单行 PK 查询。
type Entitlements struct {
	OrgID         uint64 `gorm:"column:org_id;primaryKey"`
	FeatureImages bool   `gorm:"column:feature_images"`
	FeatureVideo  bool   `gorm:"column:feature_video"`
	MaxMembers    int    `gorm:"column:max_members"`  // 0=不限
	MaxAPIKeys    int    `gorm:"column:max_api_keys"` // 0=不限
}

func (Entitlements) TableName() string { return "org_entitlements" }

// ErrMemberLimit / ErrKeyLimit：资源上限触顶（强制点在同事务内 COUNT 比对，原子无 TOCTOU）。
var (
	ErrMemberLimit = errors.New("identity: organization member limit reached")
	ErrKeyLimit    = errors.New("identity: organization api key limit reached")
)

// DefaultEntitlements：无配置行时的平台默认（permissive，与 migration DEFAULT 一致）。
func DefaultEntitlements(orgID uint64) Entitlements {
	return Entitlements{OrgID: orgID, FeatureImages: true, FeatureVideo: true, MaxMembers: 0, MaxAPIKeys: 0}
}

// LoadEntitlements：取 org 配置；无行返回平台默认。
func LoadEntitlements(db *gorm.DB, orgID uint64) (Entitlements, error) {
	var e Entitlements
	err := db.Where("org_id = ?", orgID).First(&e).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return DefaultEntitlements(orgID), nil
	}
	if err != nil {
		return Entitlements{}, err
	}
	return e, nil
}

// UpsertEntitlements：超管写入/更新 org 配置（整行覆盖语义；org_id 冲突即更新各列）。
func UpsertEntitlements(db *gorm.DB, e Entitlements) error {
	return db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "org_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"feature_images", "feature_video", "max_members", "max_api_keys",
		}),
	}).Create(&e).Error
}
