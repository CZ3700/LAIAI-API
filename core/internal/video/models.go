// Package video coordinates Ark tasks and their shared financial transaction.
package video

import "time"

const Provider = "ark_official"

// ModelConfig is operator-controlled. It must never contain credentials.
type ModelConfig struct {
	PublicModel             string `gorm:"primaryKey;column:public_model"`
	Version                 string
	ProviderScopeID         uint64
	ChannelID               uint64
	Enabled                 bool
	CapabilityJSON          string `gorm:"column:capability_json"`
	PricingVersion          string
	NoChargeStatuses        string
	MaxInputVideoDurationMS int64
	BoundsJSON              string `gorm:"column:bounds_json"`
	VerifiedAt              *time.Time
	UpdatedAt               time.Time
}

func (ModelConfig) TableName() string { return "video_model_configs" }

// State augments an existing tasks row. Raw input and signed result URLs are
// held exclusively by Invocation Store, never copied into this projection.
type State struct {
	TaskID                   string `gorm:"primaryKey;column:task_id"`
	OrgID                    uint64
	CreateRequestID          string
	ProviderScopeID          uint64
	CredentialSnapshotID     uint64
	ProviderBaseURL          string
	ConfigVersion            string
	CapabilityVersion        string
	UpstreamModel            string
	NoChargeStatuses         string
	BillingState             string
	CaptureHistory           bool
	RetentionDays            int
	SyncState                string
	ArtifactStatus           string
	EffectivePayloadRef      string
	ResponsePayloadRef       string
	ResultPayloadRef         string
	TotalTokens              *int64
	ObservedCompletionTokens *int64
	BillingSnapshotID        uint64
	IdempotencyRecordID      uint64
	ParentDraftTaskID        string
	RowVersion               uint64
	EventSequence            uint64
	LastSyncedAt             *time.Time
	NextPollAt               time.Time
	ReviewDueAt              *time.Time
	DeletedAt                *time.Time
	DeletionOperationID      string
	CreatedAt                time.Time
	UpdatedAt                time.Time
}

func (State) TableName() string { return "video_task_states" }

type IdempotencyRecord struct {
	ID          uint64 `gorm:"primaryKey"`
	OrgID       uint64
	Method      string
	Operation   string
	KeyHMAC     string `gorm:"column:key_hmac"`
	BodyHMAC    string `gorm:"column:body_hmac"`
	HashVersion string
	ResourceID  string
	OperationID string
	CreatedAt   time.Time
}

func (IdempotencyRecord) TableName() string { return "video_idempotency_records" }

// A changed secret with the same version fails closed rather than creating a
// second idempotency identity. Rotation requires an explicit version migration.
type HashVersion struct {
	Version     string `gorm:"primaryKey;column:version"`
	Fingerprint string
}

func (HashVersion) TableName() string { return "video_hash_versions" }

// CredentialSnapshot is a dedicated encrypted credential store. No credential
// bytes are serialized into task/event/payload data or exposed to customers.
type CredentialSnapshot struct {
	ID                 uint64 `gorm:"primaryKey"`
	OrgID              uint64
	TaskID             string
	ProviderScopeID    uint64
	SourceCredentialID uint64
	Ciphertext         string
	CreatedAt          time.Time
}

func (CredentialSnapshot) TableName() string { return "video_credential_snapshots" }

type ProviderScope struct {
	ID           uint64 `gorm:"primaryKey"`
	ScopeKey     string
	AccountRef   string
	Region       string
	ProjectName  string
	Enabled      bool
	FXUSDPerCNY  string `gorm:"column:fx_usd_per_cny"`
	FXVersion    string
	FXObservedAt time.Time
}

func (ProviderScope) TableName() string { return "video_provider_scopes" }

type Operation struct {
	ID               string     `gorm:"primaryKey;column:id" json:"operation_id"`
	OrgID            uint64     `json:"-"`
	ResourceID       string     `json:"resource_id"`
	Kind             string     `json:"kind"`
	Status           string     `json:"status"`
	Version          uint64     `json:"-"`
	AttemptID        string     `json:"-"`
	PayloadRef       string     `json:"-"`
	ReceiptRef       string     `json:"-"`
	HTTPStatus       int        `gorm:"column:http_status" json:"-"`
	ReceiptAppliedAt *time.Time `json:"-"`
	ReceivedOutcome  string     `json:"-"`
	ReceivedTaskID   string     `json:"-"`
	LeaseUntil       *time.Time `json:"-"`
	ErrorCode        string     `json:"-"`
	CreatedAt        time.Time  `json:"created_at"`
	UpdatedAt        time.Time  `json:"-"`
	PollURL          string     `gorm:"-" json:"poll_url"`
}

func (Operation) TableName() string { return "video_operations" }

type Attempt struct {
	ID                uint64 `gorm:"primaryKey"`
	OrgID             uint64
	TaskID            string
	AttemptID         string
	RequestID         string
	OperationID       string
	Phase             string
	HTTPStatus        int `gorm:"column:http_status"`
	UpstreamRequestID string
	EvidenceRef       string
	ErrorCode         string
	ObservedAt        time.Time
}

func (Attempt) TableName() string { return "video_attempts" }

type Event struct {
	ID           string    `gorm:"primaryKey;column:id" json:"id"`
	OrgID        uint64    `json:"-"`
	TaskID       string    `json:"-"`
	Sequence     uint64    `json:"-"`
	Type         string    `json:"type"`
	RequestID    string    `json:"request_id"`
	FromStatus   string    `json:"from_status,omitempty"`
	ToStatus     string    `json:"to_status,omitempty"`
	BillingState string    `json:"-"`
	EvidenceRef  string    `json:"-"`
	CreatedAt    time.Time `json:"created_at"`
}

func (Event) TableName() string { return "video_events" }

type Outbox struct {
	EventID       string `gorm:"primaryKey;column:event_id"`
	OrgID         uint64
	TaskID        string
	Topic         string
	Status        string
	Attempts      int
	NextAttemptAt time.Time
	CreatedAt     time.Time
}

func (Outbox) TableName() string { return "video_outbox" }

type ProviderRate struct {
	ScopeID       uint64 `gorm:"primaryKey"`
	Action        string `gorm:"primaryKey"`
	NextAllowedAt time.Time
}

func (ProviderRate) TableName() string { return "video_provider_rates" }

type UpstreamBinding struct {
	ProviderScopeID uint64 `gorm:"primaryKey"`
	UpstreamTaskID  string `gorm:"primaryKey"`
	OrgID           uint64
	TaskID          string
	CreatedAt       time.Time
}

func (UpstreamBinding) TableName() string { return "video_upstream_bindings" }

// Policy is explicit consent, including transient encrypted execution storage.
// Missing policy never silently opts an organization into body retention.
type Policy struct {
	OrgID                 uint64 `gorm:"primaryKey"`
	AsyncExecutionAllowed bool
	CaptureHistory        bool
	RetentionDays         int
	UpdatedAt             time.Time
}

func (Policy) TableName() string { return "video_org_policies" }

type TaskView struct {
	ID             string     `json:"id"`
	Model          string     `json:"model"`
	Status         string     `json:"status"`
	BillingState   string     `json:"billing_state"`
	Usage          *UsageView `json:"usage"`
	SyncState      string     `json:"sync_state"`
	ArtifactStatus string     `json:"artifact_status"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
	LastSyncedAt   *time.Time `json:"last_synced_at"`
	DeletionState  string     `json:"deletion_state"`
	ContentURL     string     `json:"content_url,omitempty"`
}
type UsageView struct {
	CompletionTokens *int64 `json:"completion_tokens"`
	TotalTokens      *int64 `json:"total_tokens"`
}
