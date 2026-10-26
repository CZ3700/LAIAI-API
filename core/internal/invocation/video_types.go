package invocation

// This file defines the durable video evidence boundary. It deliberately does
// not change Recorder: synchronous transaction errors must reach the video
// orchestrator, while existing relay callers retain their old API.

import (
	"context"
	"errors"
	"time"

	"github.com/laiai/laiai-next/core/internal/crypto"
	"gorm.io/gorm"
)

var (
	ErrVideoInvalid             = errors.New("video evidence: invalid input")
	ErrVideoConflict            = errors.New("video evidence: correlation conflict")
	ErrVideoRetentionDenied     = errors.New("video evidence: retention not permitted")
	ErrVideoNotFound            = errors.New("video evidence: not found")
	ErrVideoExpired             = errors.New("video evidence: expired")
	ErrVideoAccessDenied        = errors.New("video evidence: access denied")
	ErrVideoAuditUnavailable    = errors.New("video evidence: audit unavailable")
	ErrVideoStorage             = errors.New("video evidence: storage unavailable")
	ErrVideoCorrupt             = errors.New("video evidence: integrity check failed")
	ErrVideoSchemaUnsupported   = errors.New("video evidence: schema unsupported")
	ErrVideoTransactionRequired = errors.New("video evidence: transaction required")
)

const (
	VideoPurposeExecution = "execution"
	VideoPurposeHistory   = "history"
	VideoPurposeDelivery  = "delivery"
	VideoClientRequest    = "client_request"
	VideoEffectiveRequest = "effective_request"
	VideoUpstreamResponse = "upstream_response"
	VideoClientResponse   = "client_response"
	VideoRoleWorker       = "execution_worker"
	VideoRoleOrgReader    = "org_reader"
	VideoRoleRoot         = "root"
	VideoCaptureStaged    = "staged"
	VideoCaptureStored    = "stored"
	VideoCaptureDisabled  = "disabled"
	VideoCaptureExpired   = "expired"
	VideoFormatJSON       = "json"
	VideoFormatText       = "redacted_text_v1"
)

// VideoPayloadInput contains bodies only, never transport headers. Non-JSON
// upstream responses are captured in a redacted_text_v1 JSON envelope. A
// trusted policy resolver must explicitly set CaptureEnabled. SecretValues
// supplements structural redaction with credentials known to the caller; none
// of those values is persisted. Disabled history captures keep metadata only.
type VideoPayloadInput struct {
	OrgID                 uint64
	RequestID             string
	AttemptID             string
	Direction             string
	Purpose               string
	SchemaVersion         uint32
	Body                  []byte
	ExpiresAt             time.Time
	CaptureEnabled        bool
	SecretValues          []string
	CaptureRepresentation string
}

// PreparedVideoPayload owns ciphertext only. Reuse it when retrying a local
// transaction; it is safe to prepare before opening that transaction.
type PreparedVideoPayload struct {
	metadata VideoPayload
	staging  VideoPayloadStaging
	// Only the fixed database-clock factories set this in-memory provenance.
	// It is neither persisted nor included in immutable evidence/HMAC bytes.
	executionPrepared bool
}

func (p *PreparedVideoPayload) PayloadID() string {
	if p == nil {
		return ""
	}
	return p.metadata.PayloadID
}

// VideoPayload, VideoPayloadStaging and VideoPayloadAccess are internal storage
// models, not HTTP DTOs. Their schema is documented in README.video.md.
type VideoPayload struct {
	PayloadID             string
	OrgID                 uint64
	RequestID             string
	AttemptID             string
	CorrelationKey        string
	Direction             string
	Purpose               string
	SchemaVersion         uint32
	BodyFormat            string
	CaptureRepresentation string
	EncryptionKeyID       string
	WrappedKey            string
	Nonce                 []byte
	CiphertextSHA256      string
	PlaintextBytes        int64
	RedactionVersion      string
	CaptureStatus         string
	ObjectRef             string
	CreatedAt             time.Time
	ExpiresAt             time.Time
	CleanupAfter          *time.Time
	ObjectDeletedAt       *time.Time
}

func (VideoPayload) TableName() string { return "invocation_payloads" }

type VideoPayloadStaging struct {
	PayloadID      string
	Ciphertext     []byte
	Attempts       int
	NextAttemptAt  time.Time
	LeaseOwner     string
	LeaseExpiresAt *time.Time
	FencingToken   uint64
	CreatedAt      time.Time
}

func (VideoPayloadStaging) TableName() string { return "invocation_payload_staging" }

type VideoPayloadAccess struct {
	AccessID      string
	Phase         string
	OrgID         uint64
	PayloadID     string
	ActorRole     string
	ActorID       string
	ReasonCode    string
	OperationID   string
	Outcome       string
	SafeErrorCode string
	CreatedAt     time.Time
}

func (VideoPayloadAccess) TableName() string { return "invocation_payload_accesses" }

// VideoAccess is constructed by trusted authorization/worker code, not decoded
// from a client body. Org readers may read only the explicitly supplied OrgID;
// callers must authenticate that organization before entering this package.
type VideoAccess struct {
	ActorRole   string
	ActorID     string
	ReasonCode  string
	OperationID string
}

// VideoBlobStore stores ciphertext in a private namespace. Put must be
// idempotent for identical key+bytes and all methods must honor ctx cancellation.
// No bucket/provider credentials are accepted in payload input.
type VideoBlobStore interface {
	Put(context.Context, string, []byte) error
	Get(context.Context, string) ([]byte, error)
	Delete(context.Context, string) error
}

type VideoStoreOptions struct {
	Now             func() time.Time
	MaxPayloadBytes int
	MaxRetention    time.Duration
	BlobStore       VideoBlobStore
}

type VideoStore struct {
	db           *gorm.DB
	keyring      *crypto.Keyring
	now          func() time.Time
	maxBytes     int
	maxRetention time.Duration
	blobs        VideoBlobStore
}

func NewVideoStore(db *gorm.DB, keyring *crypto.Keyring, options VideoStoreOptions) (*VideoStore, error) {
	if db == nil || keyring == nil {
		return nil, ErrVideoInvalid
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.MaxPayloadBytes == 0 {
		options.MaxPayloadBytes = 10 << 20
	}
	if options.MaxRetention == 0 {
		options.MaxRetention = 30 * 24 * time.Hour
	}
	if options.MaxPayloadBytes < 1 || options.MaxPayloadBytes > 64<<20 || options.MaxRetention < time.Millisecond || options.MaxRetention > 30*24*time.Hour {
		return nil, ErrVideoInvalid
	}
	return &VideoStore{db: db, keyring: keyring, now: options.Now,
		maxBytes: options.MaxPayloadBytes, maxRetention: options.MaxRetention, blobs: options.BlobStore}, nil
}
