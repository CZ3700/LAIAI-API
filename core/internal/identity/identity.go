// Package identity 实现自有身份体系（设计文档 §4.1/§4.2）：
// Organization 是账户体系唯一容器，个人账户 = 单成员 org。
package identity

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/laiai/laiai-next/core/internal/dberr"
	"gorm.io/gorm"
)

var (
	ErrEmailTaken     = errors.New("identity: email already registered")
	ErrInvalidInput   = errors.New("identity: invalid input")
	ErrNotActiveMember = errors.New("identity: account inactive or membership revoked")
)

// VerifyActiveMembership：P0-1 安全闭环——校验 (user, org) 当前有效：user.status=active 且
// org.status=active 且 org_members(user,org) 仍存在；三者任一不满足返回 ErrNotActiveMember。
// 用于会话作用域控制面（requireActiveMembership）与 relay per-member key gate（VerifyKey），
// 消解「封禁/移出团队/组织禁用后旧 session 或旧 key 在 TTL 内仍可访问」的越权窗口。
func VerifyActiveMembership(db *gorm.DB, userID, orgID uint64) error {
	var n int64
	if err := db.Raw(`SELECT COUNT(*) FROM org_members m
		JOIN users u ON u.id = m.user_id
		JOIN organizations o ON o.id = m.org_id
		WHERE m.user_id = ? AND m.org_id = ? AND u.status = ? AND o.status = ?`,
		userID, orgID, StatusActive, StatusActive).Scan(&n).Error; err != nil {
		return err
	}
	if n == 0 {
		return ErrNotActiveMember
	}
	return nil
}

// VerifyUserActive：仅校验用户本身 active（封禁=全站锁死，用于账户级端点 /me /orgs /switch）。
// 用户不存在（status 扫成 0=Disabled）同样拒绝。
func VerifyUserActive(db *gorm.DB, userID uint64) error {
	var status int8
	if err := db.Raw(`SELECT status FROM users WHERE id = ?`, userID).Scan(&status).Error; err != nil {
		return err
	}
	if status != StatusActive {
		return ErrNotActiveMember
	}
	return nil
}

// VerifyOrgActive：仅校验 org active（用于无具体成员归属的 org 级 API Key）。
func VerifyOrgActive(db *gorm.DB, orgID uint64) error {
	var status int8
	if err := db.Raw(`SELECT status FROM organizations WHERE id = ?`, orgID).Scan(&status).Error; err != nil {
		return err
	}
	if status != StatusActive {
		return ErrNotActiveMember
	}
	return nil
}

const (
	OrgKindPersonal int8 = 0
	OrgKindTeam     int8 = 1

	RoleOwner   = "owner"
	RoleAdmin   = "admin"
	RoleMember  = "member"
	RoleBilling = "billing"

	StatusActive        int8 = 1
	StatusDisabled      int8 = 0
	StatusPendingVerify int8 = 2
)

type User struct {
	ID             uint64 `gorm:"primaryKey"`
	Email          string `gorm:"column:email"`
	EmailCanonical string `gorm:"column:email_canonical"`
	PasswordHash   string `gorm:"column:password_hash"`
	DisplayName    string `gorm:"column:display_name"`
	TOTPSecretEnc  string `gorm:"column:totp_secret_enc"` // 2FA 共享密钥（信封加密，0017）
	TOTPEnabled    int8   `gorm:"column:totp_enabled"`    // 2FA 是否启用
	Role           string `gorm:"column:role"`            // 平台角色（0020，RBAC）：user|support|admin|superadmin
	Status         int8   `gorm:"column:status"`
	AdminRemark    string `gorm:"column:admin_remark" json:"-"` // 运营备注（0025，gap P2-B）：仅后台可见可改，json:"-" 防原样返回时外露给用户
}

// 平台角色（0020 RBAC）。线性等级，rank 越大权越大。X-Admin-Token 视同 superadmin。
// 注意：与上面的「组织成员角色」(RoleOwner/RoleAdmin/RoleMember/RoleBilling) 是两套体系，
// 故用 PlatRole 前缀区分——这套存 users.role，决定能否进后台、进到哪一层。
const (
	PlatRoleUser       = "user"
	PlatRoleSupport    = "support"
	PlatRoleAdmin      = "admin"
	PlatRoleSuperadmin = "superadmin"
)

// RoleRank：平台角色等级（未知角色视作最低 user=0）。用于「至少 X 级」门禁。
func RoleRank(role string) int {
	switch role {
	case PlatRoleSuperadmin:
		return 3
	case PlatRoleAdmin:
		return 2
	case PlatRoleSupport:
		return 1
	default:
		return 0
	}
}

// CanManageUser：能否对目标用户执行管理操作（封禁/改角色等）。
// 对标 NewAPI（controller/user.go）规则：superadmin/root 可管理任何人；
// 其余角色仅能管理「严格更低」等级者——admin 管不了 admin/superadmin（防平级互掀/提权）。
func CanManageUser(actorRole, targetRole string) bool {
	if actorRole == PlatRoleSuperadmin {
		return true
	}
	return RoleRank(actorRole) > RoleRank(targetRole)
}

// ValidRole：是否是已知可授予的平台角色。
func ValidRole(role string) bool {
	switch role {
	case PlatRoleUser, PlatRoleSupport, PlatRoleAdmin, PlatRoleSuperadmin:
		return true
	}
	return false
}

func (User) TableName() string { return "users" }

type Organization struct {
	ID           uint64  `gorm:"primaryKey"`
	Slug         string  `gorm:"column:slug"`
	Name         string  `gorm:"column:name"`
	Kind         int8    `gorm:"column:kind"`
	WhiteLabel   int8    `gorm:"column:white_label"`
	Group        string  `gorm:"column:group_name"`     // 计费分组（0011，差异化定价）
	AffCode      *string `gorm:"column:aff_code"`       // 邀请码（0016，NULL=未生成）
	InviterOrgID uint64  `gorm:"column:inviter_org_id"` // 邀请来源组织（0016）
	Status       int8    `gorm:"column:status"`
}

func (Organization) TableName() string { return "organizations" }

type OrgMember struct {
	ID     uint64 `gorm:"primaryKey"`
	OrgID  uint64 `gorm:"column:org_id"`
	UserID uint64 `gorm:"column:user_id"`
	Role   string `gorm:"column:role"`
}

func (OrgMember) TableName() string { return "org_members" }

type RegisterInput struct {
	Email       string
	Password    string
	DisplayName string
	InviterCode string // 邀请码（0016，可选）
}

// genAffCode：8 hex 邀请码（crypto/rand）。碰撞概率可忽略，UNIQUE 兜底。
func genAffCode() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Register 创建用户 + 隐式个人 org + owner 成员关系。
// 去重的硬保证是 users.email_canonical UNIQUE（schema 层，红线）；
// HTTP 层（M1）再叠加 Redis SETNX 并发锁作为礼貌性前置，但正确性不依赖它。
func Register(tx *gorm.DB, in RegisterInput) (*User, *Organization, error) {
	canonical, err := CanonicalizeEmail(in.Email)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	hash, err := HashPassword(in.Password)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %v", ErrInvalidInput, err)
	}
	name := strings.TrimSpace(in.DisplayName)
	if name == "" {
		name = strings.SplitN(canonical, "@", 2)[0]
	}

	u := &User{
		Email:          strings.TrimSpace(in.Email),
		EmailCanonical: canonical,
		PasswordHash:   hash,
		DisplayName:    name,
		Status:         StatusActive,
	}
	if err := tx.Create(u).Error; err != nil {
		if isDuplicate(err) {
			return nil, nil, ErrEmailTaken
		}
		return nil, nil, err
	}

	// 邀请来源（0016）：邀请码 → 邀请组织 id（无效码静默忽略，不阻断注册）
	var inviterOrgID uint64
	if code := strings.TrimSpace(in.InviterCode); code != "" {
		var inviter Organization
		if tx.Where("aff_code = ?", code).First(&inviter).Error == nil {
			inviterOrgID = inviter.ID
		}
	}
	affCode := genAffCode()
	org := &Organization{
		Slug:         fmt.Sprintf("u-%d", u.ID), // 用户 ID 派生，天然唯一
		Name:         name,
		Kind:         OrgKindPersonal,
		Group:        "default", // 默认计费分组（0011）；显式设避免 GORM 零值覆盖 DB 默认
		AffCode:      &affCode,
		InviterOrgID: inviterOrgID,
		Status:       StatusActive,
	}
	if err := tx.Create(org).Error; err != nil {
		return nil, nil, err
	}
	m := &OrgMember{OrgID: org.ID, UserID: u.ID, Role: RoleOwner}
	if err := tx.Create(m).Error; err != nil {
		return nil, nil, err
	}
	return u, org, nil
}

// FindUserByEmail 用 canonical 形态查用户（登录入口统一走这里，杜绝 alias 绕过）。
func FindUserByEmail(db *gorm.DB, email string) (*User, error) {
	canonical, err := CanonicalizeEmail(email)
	if err != nil {
		return nil, err
	}
	u := &User{}
	if err := db.Where("email_canonical = ?", canonical).First(u).Error; err != nil {
		return nil, err
	}
	return u, nil
}

func isDuplicate(err error) bool { return dberr.IsDuplicate(err) }
