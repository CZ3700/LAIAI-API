// Package videoasset implements independently scheduled, tenant-scoped Ark
// asset management. It does not grant licenses from client callback data.
package videoasset

import (
	"context"
	"errors"
	"github.com/laiai/laiai-next/core/internal/crypto"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"gorm.io/gorm"
	"log/slog"
	"time"
)

var (
	ErrInvalid       = errors.New("video asset: invalid input")
	ErrNotFound      = errors.New("video asset: not found")
	ErrConflict      = errors.New("video asset: state conflict")
	ErrUnsupported   = errors.New("video asset: capability not enabled")
	ErrAuthorization = errors.New("video asset: valid portrait license required")
	ErrInUse         = errors.New("video asset: resource in use")
	ErrNotEmpty      = errors.New("video asset: group not empty")
	ErrStorage       = errors.New("video asset: storage unavailable")
	ErrSecret        = errors.New("video asset: secret unavailable")
	// ErrExecutionPolicy is a local configuration classification, never an
	// upstream credential or permission error. Console diagnostics may expose
	// the corresponding safe action without revealing account configuration.
	ErrExecutionPolicy = errors.New("video asset: explicit execution policy required")
)

const (
	KindVirtual      = "virtual"
	KindReal         = "real"
	StatusPending    = "pending"
	StatusProcessing = "processing"
	StatusActive     = "active"
	StatusFailed     = "failed"
	StatusDeleting   = "deleting"
	StatusDeleted    = "deleted"
	ReviewClear      = "clear"
	ReviewRequired   = "needs_review"
)

// Scope is a trusted immutable account/region/project routing snapshot.
type Scope struct {
	ID        string
	Version   uint64
	AccountID string
	Region    string
	Project   string
}
type Capabilities struct {
	VirtualGroups, Images, Videos, Audio, Portrait, DeleteVirtualEmptyGroups bool
	Sync, Update, DeleteAssets, ExclusiveProjectWrites                       bool
}
type Credentials struct {
	AccessKey, SecretKey, SessionToken string `json:"-"`
}

func (Credentials) String() string       { return "[redacted Ark credentials]" }
func (Credentials) GoString() string     { return "[redacted Ark credentials]" }
func (Credentials) LogValue() slog.Value { return slog.StringValue("[redacted Ark credentials]") }

type RuntimeScope struct {
	Scope                                     Scope
	Credentials                               Credentials
	Capabilities                              Capabilities
	ExecutionRetentionConsent, CaptureHistory bool
	RetentionDays                             int
}

// An empty requested scope selects the configured route; otherwise resolution
// must return the exact stored version, including the original account/project.
type ScopeResolver func(context.Context, uint64, Scope) (RuntimeScope, error)
type MediaEvidence struct {
	ImmutableObjectRef, ObjectSHA256, MIME string
	FetchURL                               string // immutable URL approved by the trusted verifier; never metadata
	ByteSize, DurationMS                   int64
	Width, Height, FPSNum, FPSDen          int
	Version                                uint64
	VerifiedAt                             time.Time
}

// The verifier must fetch safely, enforce redirects/IP limits, and verify an
// immutable object. A duration sniff of mutable public URLs is insufficient.
type MediaVerifier func(context.Context, uint64, string, string) (MediaEvidence, error)
type CallbackTarget struct{ ID, URL string }
type CallbackResolver func(context.Context, uint64, string) (CallbackTarget, error)
type Options struct {
	ResolveCallbackByID CallbackResolver
	PlatformCallbackURL string
	Now                 func() time.Time
	ResolveScope        ScopeResolver
	VerifyMedia         MediaVerifier
	ResolveCallback     CallbackResolver
	HMACKeys            map[string][]byte
	ActiveHMACKey       string
	MaxLicenseAge       time.Duration
	Client              *ManagementClient
}
type Service struct {
	db       *gorm.DB
	keys     *crypto.Keyring
	evidence *invocation.VideoStore
	now      func() time.Time
	options  Options
	console  ConsoleOptions
}

func NewService(db *gorm.DB, keys *crypto.Keyring, evidence *invocation.VideoStore, options Options) (*Service, error) {
	if db == nil || keys == nil || evidence == nil || options.ResolveScope == nil || len(options.HMACKeys[options.ActiveHMACKey]) < 32 {
		return nil, ErrInvalid
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.MaxLicenseAge == 0 {
		options.MaxLicenseAge = 24 * time.Hour
	}
	if options.MaxLicenseAge <= 0 || options.MaxLicenseAge > 7*24*time.Hour {
		return nil, ErrInvalid
	}
	if options.Client == nil {
		options.Client = NewManagementClient(nil)
	}
	copied := map[string][]byte{}
	for k, v := range options.HMACKeys {
		if !validID(k) || len(v) < 32 {
			return nil, ErrInvalid
		}
		copied[k] = append([]byte(nil), v...)
	}
	options.HMACKeys = copied
	return &Service{db: db, keys: keys, evidence: evidence, now: options.Now, options: options}, nil
}

// ORM models are never returned by HTTP handlers.
type Group struct {
	ID                                                                    string `gorm:"column:group_id"`
	OrgID                                                                 uint64
	Kind, Name, ScopeID                                                   string
	ScopeVersion                                                          uint64
	AccountID, Region, Project                                            string
	ProviderID                                                            *string
	Status, ReviewState, PortraitID, CreateOperationID, DeleteOperationID string
	RowVersion                                                            uint64
	DeletedAt                                                             *time.Time
	CreatedAt, UpdatedAt                                                  time.Time
}

func (Group) TableName() string { return "video_asset_groups" }

type Asset struct {
	ID                                                                           string `gorm:"column:asset_id"`
	OrgID                                                                        uint64
	GroupID, MediaType, Name, ScopeID                                            string
	ScopeVersion                                                                 uint64
	ProviderID                                                                   *string
	Status, ReviewState, SourcePayloadID, ImmutableObjectRef, ObjectSHA256, MIME string
	ByteSize, DurationMS                                                         int64
	Width, Height, FPSNum, FPSDen                                                int
	MetadataVersion                                                              uint64
	VerifiedAt                                                                   time.Time
	CreateOperationID, SafeErrorCode                                             string
	RowVersion                                                                   uint64
	DeletedAt                                                                    *time.Time
	CreatedAt, UpdatedAt                                                         time.Time
}

func (Asset) TableName() string { return "video_assets" }

type TaskReference struct {
	OrgID                                      uint64
	TaskID, AssetID, GroupID                   string
	ContentIndex                               int
	Role                                       string
	AssetMetadataVersion, AuthorizationVersion uint64
	ReleasedAt                                 *time.Time
	CreatedAt                                  time.Time
}

func (TaskReference) TableName() string { return "video_task_asset_refs" }

type Portrait struct {
	CallbackStateHash, CallbackStateVersion                                      string
	CallbackUsedAt                                                               *time.Time
	ID                                                                           string `gorm:"column:session_id"`
	OrgID, OwnerUserID, CreatedByKeyID                                           uint64
	ScopeID                                                                      string
	ScopeVersion                                                                 uint64
	AccountID, Region, Project                                                   string
	SessionStatus, LicenseStatus, CreateOperationID, CallbackTargetID, SecretRef string
	SessionExpiresAt                                                             *time.Time
	GroupID                                                                      *string
	ValidFrom, ValidUntil                                                        *time.Time
	LicenseScope                                                                 string
	LicenseVersion                                                               uint64
	LicenseEvidenceRef, LicenseSource                                            string
	LastVerifiedAt, ReverifyDueAt, RevokedAt                                     *time.Time
	RowVersion                                                                   uint64
	CreatedAt, UpdatedAt                                                         time.Time
}

func (Portrait) TableName() string { return "video_portrait_authorizations" }

type Secret struct {
	ID                     string `gorm:"column:secret_id"`
	OrgID                  uint64
	SessionID, ValueCipher string
	ExpiresAt              time.Time
	DestroyedAt            *time.Time
	CreatedAt              time.Time
}

func (Secret) TableName() string { return "video_portrait_secrets" }

type Event struct {
	ID                                                                string `gorm:"column:event_id"`
	OrgID                                                             uint64
	ResourceType, ResourceID, OperationID, Kind, EvidenceRef, ActorID string
	CreatedAt                                                         time.Time
}

func (Event) TableName() string { return "video_asset_events" }

type Operation struct {
	ID                                                                                                    string `gorm:"column:operation_id"`
	OrgID                                                                                                 uint64
	Action, ResourceType, ResourceID, RequestID, CanonicalPath, HMACKeyVersion, IdempotencyHash, BodyHash string
	ScopeID                                                                                               string
	ScopeVersion                                                                                          uint64
	AccountID, Region, Project                                                                            string
	Status, PayloadID, ResponsePayloadID, LeaseOwner                                                      string
	LeaseExpiresAt                                                                                        *time.Time
	Fence                                                                                                 uint64
	HTTPStatus                                                                                            int
	UpstreamRequestID, SafeErrorCode, Receipt                                                             string
	CreatedAt, UpdatedAt, NextAttemptAt                                                                   time.Time
}

func (Operation) TableName() string { return "video_asset_operations" }

type TenantLock struct {
	OrgID   uint64
	Version uint64
}

func (TenantLock) TableName() string { return "video_asset_tenant_locks" }

type GroupDTO struct {
	ID                          string        `json:"id"`
	Version                     uint64        `json:"version"`
	Kind                        string        `json:"kind"`
	Name                        string        `json:"name"`
	Status                      string        `json:"status"`
	AssetCount                  int64         `json:"asset_count"`
	DeletionMode                string        `json:"deletion_mode"`
	DeletionBlockReason         string        `json:"deletion_block_reason"`
	DeleteOperation             *OperationDTO `json:"delete_operation,omitempty"`
	AuthorizationStatus         string        `json:"authorization_status"`
	AuthorizationLastVerifiedAt *time.Time    `json:"authorization_last_verified_at,omitempty"`
	AuthorizationValidUntil     *time.Time    `json:"authorization_valid_until,omitempty"`
	CreatedAt                   time.Time     `json:"created_at"`
	UpdatedAt                   time.Time     `json:"updated_at"`
}
type AssetDTO struct {
	ID        string    `json:"id"`
	Version   uint64    `json:"version"`
	GroupID   string    `json:"group_id"`
	MediaType string    `json:"media_type"`
	Name      string    `json:"name"`
	Status    string    `json:"status"`
	Reference string    `json:"reference,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
type PortraitDTO struct {
	ID               string     `json:"session_id"`
	Status           string     `json:"status"`
	GroupID          *string    `json:"group_id,omitempty"`
	AuthorizationURL string     `json:"authorization_url,omitempty"`
	ExpiresAt        *time.Time `json:"expires_at"`
	CreatedAt        time.Time  `json:"created_at"`
}
type SafeError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}
type OperationDTO struct {
	ID          string     `json:"operation_id"`
	RequestID   string     `json:"request_id"`
	Kind        string     `json:"kind"`
	ResourceID  string     `json:"resource_id"`
	Status      string     `json:"status"`
	PollURL     string     `json:"poll_url"`
	Error       *SafeError `json:"error,omitempty"`
	CreatedAt   time.Time  `json:"created_at"`
	CompletedAt *time.Time `json:"completed_at"`
}
type CreateContext struct {
	CallerKind                string
	OrgID, UserID, KeyID      uint64
	RequestID, IdempotencyKey string
}
type GroupCreate struct {
	Description string `json:"description,omitempty"`
	Kind        string `json:"kind"`
	Name        string `json:"name"`
}
type AssetCreate struct {
	GroupID   string `json:"group_id"`
	URL       string `json:"url"`
	MediaType string `json:"media_type"`
	Name      string `json:"name,omitempty"`
}
type PortraitCreate struct {
	Name        string `json:"name,omitempty"`
	Description string `json:"description,omitempty"`
	CallbackURL string `json:"callback_url"`
}
type ReferenceInput struct {
	AssetID, Role string
	ContentIndex  int
}
type Binding struct {
	Role                                  string
	ContentIndex                          int
	AssetID, ProviderAssetID, MediaType   string
	DurationMS                            int64
	Width, Height, FPSNum, FPSDen         int
	MetadataVersion, AuthorizationVersion uint64
}
