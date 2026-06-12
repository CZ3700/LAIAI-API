package identity

import (
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"
)

// Organizations/Teams 领域逻辑（设计 §4.2）：
// 个人 = 单成员 org；团队 = 多成员 org（共享余额、角色、per-member key）。

var (
	ErrNotMember       = errors.New("identity: user is not a member of this organization")
	ErrAlreadyMember   = errors.New("identity: user is already a member")
	ErrUserNotFound    = errors.New("identity: no account with this email")
	ErrLastOwner       = errors.New("identity: cannot remove the only owner")
	ErrForbiddenRole   = errors.New("identity: role not assignable via this API")
	ErrInsufficientRole = errors.New("identity: requires owner or admin role")
)

// Membership：用户视角的 org 列表行。
type Membership struct {
	OrgID uint64 `json:"org_id"`
	Name  string `json:"name"`
	Kind  int8   `json:"kind"`
	Role  string `json:"role"`
}

// OrgsOf 返回用户的全部 org（含角色）。
func OrgsOf(db *gorm.DB, userID uint64) ([]Membership, error) {
	var out []Membership
	err := db.Table("org_members").
		Select("org_members.org_id, organizations.name, organizations.kind, org_members.role").
		Joins("JOIN organizations ON organizations.id = org_members.org_id").
		Where("org_members.user_id = ? AND organizations.status = 1", userID).
		Order("org_members.org_id").
		Scan(&out).Error
	return out, err
}

// RoleIn 返回用户在 org 中的角色；非成员返回 ErrNotMember。
func RoleIn(db *gorm.DB, orgID, userID uint64) (string, error) {
	m := &OrgMember{}
	if err := db.Where("org_id = ? AND user_id = ?", orgID, userID).First(m).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return "", ErrNotMember
		}
		return "", err
	}
	return m.Role, nil
}

// CanManageMembers：成员管理权限门（owner/admin）。
func CanManageMembers(role string) bool { return role == RoleOwner || role == RoleAdmin }

// CreateTeamOrg 建团队 org，创建者为 owner。
func CreateTeamOrg(tx *gorm.DB, ownerID uint64, name string) (*Organization, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 64 {
		return nil, fmt.Errorf("%w: name required (max 64 chars)", ErrInvalidInput)
	}
	org := &Organization{Slug: "", Name: name, Kind: OrgKindTeam, Status: StatusActive}
	// slug 由 org id 派生（与个人 org 同款策略，天然唯一）：先建行再补 slug
	if err := tx.Create(org).Error; err != nil {
		return nil, err
	}
	org.Slug = fmt.Sprintf("t-%d", org.ID)
	if err := tx.Model(org).Update("slug", org.Slug).Error; err != nil {
		return nil, err
	}
	if err := tx.Create(&OrgMember{OrgID: org.ID, UserID: ownerID, Role: RoleOwner}).Error; err != nil {
		return nil, err
	}
	return org, nil
}

// AddMemberByEmail：按邮箱加成员（对方必须已注册；查找走 canonical 形态，alias 绕不过）。
// 可指派角色限 admin|member|billing——owner 不经此 API 产生（所有权转移是单独流程）。
func AddMemberByEmail(tx *gorm.DB, orgID uint64, email, role string) (*User, error) {
	switch role {
	case RoleAdmin, RoleMember, RoleBilling:
	default:
		return nil, ErrForbiddenRole
	}
	u, err := FindUserByEmail(tx, email)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrUserNotFound
		}
		return nil, err
	}
	m := &OrgMember{OrgID: orgID, UserID: u.ID, Role: role}
	if err := tx.Create(m).Error; err != nil {
		if isDuplicate(err) { // UNIQUE(org_id, user_id)
			return nil, ErrAlreadyMember
		}
		return nil, err
	}
	return u, nil
}

// RemoveMember：移除成员。末位 owner 保护——org 永远至少留一个 owner。
func RemoveMember(tx *gorm.DB, orgID, userID uint64) error {
	role, err := RoleIn(tx, orgID, userID)
	if err != nil {
		return err
	}
	if role == RoleOwner {
		var owners int64
		if err := tx.Model(&OrgMember{}).
			Where("org_id = ? AND role = ?", orgID, RoleOwner).Count(&owners).Error; err != nil {
			return err
		}
		if owners <= 1 {
			return ErrLastOwner
		}
	}
	return tx.Where("org_id = ? AND user_id = ?", orgID, userID).Delete(&OrgMember{}).Error
}
