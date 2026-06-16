package identity

import (
	"errors"
	"fmt"
	"strings"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
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
	ErrInvitePending   = errors.New("identity: an invitation for this user is already pending")
	ErrInviteNotFound  = errors.New("identity: invitation not found")
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

// InviteMember：按邮箱发出团队邀请（对方必须已注册；查找走 canonical 形态，alias 绕不过）。
// 不再直接拉人入伙——只落一条 pending 邀请，被邀请人 AcceptInvitation 才真正成为成员
// （未经同意拉人 = 让其访问团队共享余额，红线）。可指派角色限 admin|member|billing——
// owner 不经此流程产生（所有权转移是单独流程）。maxMembers 这里做软预检（UX 早反馈），
// 真正权威的上限校验在 accept 时再做一次（邀请期间团队可能已满）。
func InviteMember(tx *gorm.DB, orgID, inviterUserID uint64, email, role string, maxMembers int, actorRole string, now int64) (*OrgInvitation, *User, error) {
	switch role {
	case RoleAdmin, RoleMember, RoleBilling:
	default:
		return nil, nil, ErrForbiddenRole
	}
	if !CanAssignRole(actorRole, role) { // BUG-H3：admin 不能授 admin（横向提权）；仅 owner 可授 admin
		return nil, nil, ErrCannotManageTarget
	}
	u, err := FindUserByEmail(tx, email)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, ErrUserNotFound
		}
		return nil, nil, err
	}
	// 已是成员？（按角色查存在性；ErrNotMember 之外的错误透传）
	if _, err := RoleIn(tx, orgID, u.ID); err == nil {
		return nil, nil, ErrAlreadyMember
	} else if !errors.Is(err, ErrNotMember) {
		return nil, nil, err
	}
	// 成员上限软预检（同事务 COUNT 现有成员）
	if maxMembers > 0 {
		var n int64
		if err := tx.Model(&OrgMember{}).Where("org_id = ?", orgID).Count(&n).Error; err != nil {
			return nil, nil, err
		}
		if n >= int64(maxMembers) {
			return nil, nil, ErrMemberLimit
		}
	}
	inv := &OrgInvitation{OrgID: orgID, InviterUserID: inviterUserID, InviteeUserID: u.ID, Role: role, CreatedAt: now}
	if err := tx.Create(inv).Error; err != nil {
		if isDuplicate(err) { // UNIQUE(org_id, invitee_user_id)：已有待处理邀请
			return nil, nil, ErrInvitePending
		}
		return nil, nil, err
	}
	return inv, u, nil
}

// InvitationView：被邀请人视角的待处理邀请行（含团队名 + 邀请人邮箱）。
type InvitationView struct {
	ID           uint64 `json:"id"`
	OrgID        uint64 `json:"org_id"`
	OrgName      string `json:"org_name"`
	Role         string `json:"role"`
	InviterEmail string `json:"inviter_email"`
	CreatedAt    int64  `json:"created_at"`
}

// InvitationsForUser：列出某用户全部待处理邀请（团队仍 active 才列）。
func InvitationsForUser(db *gorm.DB, userID uint64) ([]InvitationView, error) {
	var out []InvitationView
	err := db.Table("org_invitations").
		Select("org_invitations.id, org_invitations.org_id, organizations.name AS org_name, org_invitations.role, users.email AS inviter_email, org_invitations.created_at").
		Joins("JOIN organizations ON organizations.id = org_invitations.org_id").
		Joins("JOIN users ON users.id = org_invitations.inviter_user_id").
		Where("org_invitations.invitee_user_id = ? AND organizations.status = 1", userID).
		Order("org_invitations.id DESC").
		Scan(&out).Error
	return out, err
}

// OrgInvitationView：管理者视角——本团队尚未确认的邀请（含被邀请人邮箱）。
type OrgInvitationView struct {
	ID           uint64 `json:"id"`
	Email        string `json:"email"` // 被邀请人邮箱
	Role         string `json:"role"`
	InviterEmail string `json:"inviter_email"`
	CreatedAt    int64  `json:"created_at"`
}

// InvitationsForOrg：列出某团队全部待处理邀请（管理者查看/撤销用）。
func InvitationsForOrg(db *gorm.DB, orgID uint64) ([]OrgInvitationView, error) {
	var out []OrgInvitationView
	err := db.Table("org_invitations").
		Select("org_invitations.id, invitee.email AS email, org_invitations.role, inviter.email AS inviter_email, org_invitations.created_at").
		Joins("JOIN users invitee ON invitee.id = org_invitations.invitee_user_id").
		Joins("JOIN users inviter ON inviter.id = org_invitations.inviter_user_id").
		Where("org_invitations.org_id = ?", orgID).
		Order("org_invitations.id DESC").
		Scan(&out).Error
	return out, err
}

// AcceptInvitation：被邀请人接受邀请 → 真正建立成员关系。
// 安全：① 仅本人能接受（按 invitee_user_id 过滤查询，查不到=ErrInviteNotFound）；
// ② 团队仍须 active；③ accept 时再做一次权威成员上限校验（邀请期间团队可能已满）；
// ④ 成员表 UNIQUE(org_id,user_id) 兜底——即使并发重复 accept 也至多一条成员。
// 成功后删除邀请行。返回新成员所属 org（供签发新会话/审计）。
// 成员上限取邀请所属 org 的 entitlements（团队上限，非被邀请人个人 org），内部加载。
func AcceptInvitation(tx *gorm.DB, invID, userID uint64) (*OrgInvitation, error) {
	inv := &OrgInvitation{}
	if err := tx.Where("id = ? AND invitee_user_id = ?", invID, userID).First(inv).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrInviteNotFound
		}
		return nil, err
	}
	if err := VerifyOrgActive(tx, inv.OrgID); err != nil {
		return nil, err // 团队被禁用 → ErrNotActiveMember
	}
	// 串行化并发 accept（堵 max_members TOCTOU 超员，对抗评审 MEDIUM）：先行锁该 org 行
	// （MySQL FOR UPDATE）。两个被邀请人同时 accept 接近满员的团队时，第二个会等第一个提交后再
	// COUNT → 看到已更新计数，上限严格生效。SQLite（测试，单连接串行写）不支持也无需，故按方言跳过。
	if tx.Dialector.Name() == "mysql" {
		var locked Organization
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
			Where("id = ?", inv.OrgID).First(&locked).Error; err != nil {
			return nil, err
		}
	}
	ent, err := LoadEntitlements(tx, inv.OrgID)
	if err != nil {
		return nil, err
	}
	if ent.MaxMembers > 0 {
		var n int64
		if err := tx.Model(&OrgMember{}).Where("org_id = ?", inv.OrgID).Count(&n).Error; err != nil {
			return nil, err
		}
		if n >= int64(ent.MaxMembers) {
			return nil, ErrMemberLimit
		}
	}
	m := &OrgMember{OrgID: inv.OrgID, UserID: userID, Role: inv.Role}
	if err := tx.Create(m).Error; err != nil {
		if isDuplicate(err) { // 已是成员（并发/重复）——清掉邀请即可，幂等
			_ = tx.Where("id = ?", invID).Delete(&OrgInvitation{}).Error
			return nil, ErrAlreadyMember
		}
		return nil, err
	}
	if err := tx.Where("id = ?", invID).Delete(&OrgInvitation{}).Error; err != nil {
		return nil, err
	}
	return inv, nil
}

// DeclineInvitation：被邀请人拒绝邀请（仅本人，按 invitee_user_id 过滤；删除该行）。
func DeclineInvitation(tx *gorm.DB, invID, userID uint64) error {
	res := tx.Where("id = ? AND invitee_user_id = ?", invID, userID).Delete(&OrgInvitation{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrInviteNotFound
	}
	return nil
}

// RevokeInvitation：管理者撤销本团队的待处理邀请（按 org_id 过滤，跨团队撤不动）。
func RevokeInvitation(tx *gorm.DB, invID, orgID uint64) error {
	res := tx.Where("id = ? AND org_id = ?", invID, orgID).Delete(&OrgInvitation{})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected == 0 {
		return ErrInviteNotFound
	}
	return nil
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
