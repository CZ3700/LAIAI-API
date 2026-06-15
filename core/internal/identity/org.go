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
	ErrCannotManageTarget = errors.New("identity: cannot manage or assign a member of equal or higher role")
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

// OrgRoleRank：团队角色等级 owner>admin>member=billing。用于「不能管理同级/更高级成员」守卫。
func OrgRoleRank(role string) int {
	switch role {
	case RoleOwner:
		return 3
	case RoleAdmin:
		return 2
	case RoleMember, RoleBilling:
		return 1
	default:
		return 0
	}
}

// CanManageMember：actor 能否对 target 成员执行管理（移除）。owner 可管理任何人；其余仅当
// 严格高于 target——admin 可管 member/billing，但**不能动 owner 或平级 admin**（BUG-H3 越权修复，
// 与平台侧 CanManageUser 同范式）。自我移除（leave）由调用方单独放行。
func CanManageMember(actorRole, targetRole string) bool {
	if actorRole == RoleOwner {
		return true
	}
	return OrgRoleRank(actorRole) > OrgRoleRank(targetRole)
}

// CanAssignRole：actor 能否授予 newRole。owner 可授 admin/member/billing；admin 只能授
// member/billing（不能造平级 admin，防横向提权）。owner 角色永不经此 API 指派。
func CanAssignRole(actorRole, newRole string) bool {
	if actorRole == RoleOwner {
		return true
	}
	if actorRole == RoleAdmin {
		return newRole == RoleMember || newRole == RoleBilling
	}
	return false
}

// CreateTeamOrg 建团队 org，创建者为 owner。
func CreateTeamOrg(tx *gorm.DB, ownerID uint64, name string) (*Organization, error) {
	name = strings.TrimSpace(name)
	if name == "" || len(name) > 64 {
		return nil, fmt.Errorf("%w: name required (max 64 chars)", ErrInvalidInput)
	}
	org := &Organization{Slug: "", Name: name, Kind: OrgKindTeam, Group: "default", Status: StatusActive}
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
func AddMemberByEmail(tx *gorm.DB, orgID uint64, email, role string, maxMembers int, actorRole string) (*User, error) {
	switch role {
	case RoleAdmin, RoleMember, RoleBilling:
	default:
		return nil, ErrForbiddenRole
	}
	if !CanAssignRole(actorRole, role) { // BUG-H3：admin 不能授 admin（横向提权）；仅 owner 可授 admin
		return nil, ErrCannotManageTarget
	}
	// 成员上限（entitlements）：同事务内 COUNT 现有成员，触顶即拒（原子，无 TOCTOU）。
	if maxMembers > 0 {
		var n int64
		if err := tx.Model(&OrgMember{}).Where("org_id = ?", orgID).Count(&n).Error; err != nil {
			return nil, err
		}
		if n >= int64(maxMembers) {
			return nil, ErrMemberLimit
		}
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

// RemoveMember：移除成员。等级守卫（BUG-H3）：actor 不能移除 owner/平级 admin（自我离队除外）；
// 末位 owner 保护——org 永远至少留一个 owner。
func RemoveMember(tx *gorm.DB, orgID uint64, actorRole string, actorUserID, userID uint64) error {
	role, err := RoleIn(tx, orgID, userID)
	if err != nil {
		return err
	}
	if userID != actorUserID && !CanManageMember(actorRole, role) { // 非自我移除才查等级
		return ErrCannotManageTarget
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
