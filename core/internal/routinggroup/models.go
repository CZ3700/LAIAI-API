// Package routinggroup owns routing configuration, not billing groups or execution.
// G1 does not authorize any grouped API key. No HTTP route exposes these commands yet.
package routinggroup

import "time"

const (
	Draft    = "draft"
	Enabled  = "enabled"
	Disabled = "disabled"
	Revoked  = "revoked"
)

type Group struct {
	ID            uint64 `gorm:"primaryKey"`
	Slug          string
	Name          string
	Description   string
	OwnerOrgID    uint64
	Status        string
	PolicyVersion uint64
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

func (Group) TableName() string { return "routing_groups" }

type Binding struct {
	GroupID   uint64 `gorm:"primaryKey;autoIncrement:false"`
	ChannelID uint64 `gorm:"primaryKey;autoIncrement:false"`
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (Binding) TableName() string { return "channel_group_bindings" }

type Grant struct {
	ID           uint64 `gorm:"primaryKey"`
	OrgID        uint64
	GroupID      uint64
	Status       string
	GrantVersion uint64
	DefaultSlot  *uint8
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

func (Grant) TableName() string { return "org_group_grants" }

// Event has no arbitrary payload. Keep it until a consumer and retention
// contract exist; it is not evidence that other nodes invalidated their cache.
type Event struct {
	ID               uint64 `gorm:"primaryKey"`
	AggregateType    string
	AggregateID      uint64
	AggregateVersion uint64
	EventKind        string
	OrgID            *uint64
	GroupID          *uint64
	ChannelID        *uint64
	KeyID            *uint64
	ActorUserID      uint64
	RequestID        string
	CreatedAt        time.Time
}

func (Event) TableName() string { return "routing_policy_events" }
