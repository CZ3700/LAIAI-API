// Package identity 实现自有身份体系（设计文档 §4.1/§4.2）：
// Organization 是账户体系唯一容器，个人账户 = 单成员 org。
package identity

import (
	"errors"
	"fmt"
	"strings"

	"github.com/laiai/laiai-next/core/internal/dberr"
	"gorm.io/gorm"
)

var (
	ErrEmailTaken   = errors.New("identity: email already registered")
	ErrInvalidInput = errors.New("identity: invalid input")
)

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
	Status         int8   `gorm:"column:status"`
}

func (User) TableName() string { return "users" }

type Organization struct {
	ID         uint64 `gorm:"primaryKey"`
	Slug       string `gorm:"column:slug"`
	Name       string `gorm:"column:name"`
	Kind       int8   `gorm:"column:kind"`
	WhiteLabel int8   `gorm:"column:white_label"`
	Group      string `gorm:"column:group_name"` // 计费分组（0011，差异化定价）
	Status     int8   `gorm:"column:status"`
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

	org := &Organization{
		Slug:   fmt.Sprintf("u-%d", u.ID), // 用户 ID 派生，天然唯一
		Name:   name,
		Kind:   OrgKindPersonal,
		Group:  "default", // 默认计费分组（0011）；显式设避免 GORM 零值覆盖 DB 默认
		Status: StatusActive,
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
