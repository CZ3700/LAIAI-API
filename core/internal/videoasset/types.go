// Package videoasset implements independently scheduled, tenant-scoped Ark
// asset management. It does not grant licenses from client callback data.
package videoasset

import (
	"strings"

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
	// ErrUnconfigured marks platform-side configuration gaps (portrait return
	// callback not installed). It is a server state, never a client mistake,
	// and must not surface as the client-blaming invalid_request envelope.
	ErrUnconfigured = errors.New("video asset: gateway callback configuration missing")
	// ErrCallbackNotRegistered marks a callback_url that does not exactly match
	// a pre-registered HTTPS target for the organization. Distinct from
	// ErrInvalid so the response can name the actual offending field.
	ErrCallbackNotRegistered = errors.New("video asset: callback target not registered")
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
	RouteSource                               string
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
	// AutoLicense：认证成功即自动登记 video_generation 业务肖像许可
	//（来源 verified_provider_license，证据为官方核实响应），并以官方
	// 素材组状态（GetAssetGroup Active）在复验到期时自动续验。
	AutoLicense         bool
	AutoLicenseValidity time.Duration
	// WriteConcurrency/QueryConcurrency：官方调用的读写分池并发度（默认 8/24）。
	// 速率纪律由 ratelimit.go 的官方限流表（单请求间隔）保证，与并发解耦；
	// 分池使注册洪峰时 CreateAsset 的 QPM 通道不被查询确认挤占，反之亦然。
	WriteConcurrency int
	QueryConcurrency int
	// Archive*：素材自动归档。每日本地时区窗口内，对近 ArchiveUsageWindowDays
	// 天内无生成任务引用的 active 素材分批注入 DeleteAsset op（官方存储同步
	// 释放、本地记录 tombstone）。真人组默认豁免（授权肖像是长期资产）；
	// ArchiveNow 供测试注入时钟。
	ArchiveEnabled     bool
	ArchiveTimezone    string
	ArchiveWindowStart string
	ArchiveWindowEnd   string
	ArchiveDailyLimit  int
	ArchiveBatch       int
	// ArchiveUsageWindowDays：归档唯一判据（2026-11-12 用户拍板）——近 N 天内
	// 有生成任务引用（video_task_asset_refs，含已终态任务）的素材不归档，
	// 无调用才归档。<=0 时默认 7。
	ArchiveUsageWindowDays  int
	ArchiveExemptRealGroups bool
	ArchiveLocation         *time.Location
	ArchiveNow              func() time.Time
	Client                  *ManagementClient
}
type Service struct {
	db       *gorm.DB
	keys     *crypto.Keyring
	evidence *invocation.VideoStore
	now      func() time.Time
	options  Options
	rates    *rateLimiter
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
	if options.AutoLicenseValidity == 0 {
		options.AutoLicenseValidity = 365 * 24 * time.Hour
	}
	if options.AutoLicenseValidity <= 0 || options.AutoLicenseValidity > 10*365*24*time.Hour {
		return nil, ErrInvalid
	}
	if options.WriteConcurrency <= 0 || options.WriteConcurrency > 64 {
		options.WriteConcurrency = 8
	}
	if options.QueryConcurrency <= 0 || options.QueryConcurrency > 64 {
		options.QueryConcurrency = 24
	}
	// 归档默认值：北京时区 02:00-06:00 窗口、每晚 ≤2 万、保留 3 天、
	// 每 tick 注入 25（对齐 DeleteAsset 10 QPS 闸余量）、真人组豁免。
	loc := options.ArchiveLocation
	if loc == nil {
		var err error
		loc, err = time.LoadLocation(defaultArchiveTimezone(options.ArchiveTimezone))
		if err != nil {
			return nil, ErrInvalid
		}
	}
	options.ArchiveLocation = loc
	if options.ArchiveWindowStart == "" {
		options.ArchiveWindowStart = "02:00"
	}
	if options.ArchiveWindowEnd == "" {
		options.ArchiveWindowEnd = "06:00"
	}
	if _, ok := parseArchiveClock(options.ArchiveWindowStart); !ok {
		return nil, ErrInvalid
	}
	if _, ok := parseArchiveClock(options.ArchiveWindowEnd); !ok {
		return nil, ErrInvalid
	}
	if options.ArchiveDailyLimit <= 0 {
		options.ArchiveDailyLimit = 20000
	}
	if options.ArchiveBatch <= 0 || options.ArchiveBatch > 500 {
		options.ArchiveBatch = 25
	}
	if options.ArchiveUsageWindowDays <= 0 {
		options.ArchiveUsageWindowDays = 7
	}
	if options.ArchiveNow == nil {
		options.ArchiveNow = time.Now
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
	return &Service{db: db, keys: keys, evidence: evidence, now: options.Now, options: options, rates: newRateLimiter(options.Now)}, nil
}

// defaultArchiveTimezone：空值回退 Asia/Shanghai（与供应商日/月预算口径一致）。
func defaultArchiveTimezone(v string) string {
	if strings.TrimSpace(v) == "" {
		return "Asia/Shanghai"
	}
	return v
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
	VerifiedAt                                                                   time.Time `gorm:"default:null"`
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
	CreateOperation             *OperationDTO `json:"create_operation,omitempty"`
	DeleteOperation             *OperationDTO `json:"delete_operation,omitempty"`
	AuthorizationStatus         string        `json:"authorization_status"`
	AuthorizationLastVerifiedAt *time.Time    `json:"authorization_last_verified_at,omitempty"`
	AuthorizationValidUntil     *time.Time    `json:"authorization_valid_until,omitempty"`
	CreatedAt                   time.Time     `json:"created_at"`
	UpdatedAt                   time.Time     `json:"updated_at"`
}
type AssetDTO struct {
	ID                  string        `json:"id"`
	Version             uint64        `json:"version"`
	GroupID             string        `json:"group_id"`
	MediaType           string        `json:"media_type"`
	Name                string        `json:"name"`
	Status              string        `json:"status"`
	Reference           string        `json:"reference,omitempty"`
	CreateOperation     *OperationDTO `json:"create_operation,omitempty"`
	DeleteOperation     *OperationDTO `json:"delete_operation,omitempty"`
	DeletionMode        string        `json:"deletion_mode"`
	DeletionBlockReason string        `json:"deletion_block_reason"`
	CreatedAt           time.Time     `json:"created_at"`
	UpdatedAt           time.Time     `json:"updated_at"`
}
type PortraitDTO struct {
	ID                 string     `json:"session_id"`
	Status             string     `json:"status"`
	GroupID            *string    `json:"group_id,omitempty"`
	AuthorizationURL   string     `json:"authorization_url,omitempty"`
	ExpiresAt          *time.Time `json:"expires_at"`
	CreatedAt          time.Time  `json:"created_at"`
	Error              *SafeError `json:"error,omitempty"`
	RequestID          string     `json:"request_id,omitempty"`
	UpstreamRequestID  string     `json:"upstream_request_id,omitempty"`
	ProviderHTTPStatus int        `json:"provider_http_status,omitempty"`
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
	// Set only by a live named-session administrator check, never from a body.
	Superadmin bool
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
