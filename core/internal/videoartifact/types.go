// Package videoartifact transfers official output into private platform storage.
// Generation state, billing and HTTP authorization remain the caller's concern.
package videoartifact

import (
	"context"
	"errors"
	"io"
	"time"
)

// ErrSourceDeferred means the resolver did not send an upstream request. It
// permits scheduling later without spending the bounded transfer attempt.
// Never return it after a network request or download may have occurred.
var ErrSourceDeferred = errors.New("artifact_source_deferred")

type Kind string

const (
	Video     Kind = "video"
	LastFrame Kind = "last_frame"
)

type Status string

const (
	Pending Status = "pending"
	Ready   Status = "ready"
	Failed  Status = "failed"
	Expired Status = "expired"
)

// Error contains safe fixed codes only; URL, storage paths and provider errors
// must never be forwarded to a client or an ordinary log through this error.
type Error struct {
	Code       string
	HTTPStatus int
	Retryable  bool
}

func (e *Error) Error() string { return e.Code }

func failure(code string, status int, retry bool) error {
	return &Error{Code: code, HTTPStatus: status, Retryable: retry}
}

// Record is INTERNAL metadata, not a public DTO. ObjectKey and lease details
// must not be exposed. Source URLs are intentionally absent.
type Record struct {
	OrgID      uint64 `gorm:"primaryKey;autoIncrement:false"`
	TaskID     string `gorm:"primaryKey"`
	Kind       Kind   `gorm:"primaryKey"`
	Status     Status
	ObjectKey  string
	SHA256     string
	Size       int64
	MediaType  string
	Width      *int
	Height     *int
	Fence      uint64
	LeaseToken string
	LeaseUntil *time.Time
	Attempts   int
	ErrorCode  string
	ExpiresAt  time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
	DeletedAt  *time.Time
	CleanedAt  *time.Time
}

func (Record) TableName() string { return "video_artifacts" }

// Spec is provided by the authenticated task orchestration after succeeded has
// been evidenced. LastFrame is registered only if requested and actually present.
// Video dimensions, if known, come from trusted official output metadata.
type Spec struct {
	OrgID     uint64
	TaskID    string
	Kind      Kind
	ExpiresAt time.Time
	Width     *int
	Height    *int
}

type Lease struct {
	Record Record
	Token  string
	Fence  uint64
}

type Metadata struct {
	MediaType string
	Size      int64
	SHA256    string
	Width     *int
	Height    *int
}

// SourceResolver returns a short-lived secret URL using a controlled provider
// query or execution evidence. It must independently enforce organization/task
// ownership and artifact kind. Historical payload capture need not be enabled.
type SourceResolver interface {
	ResolveSource(context.Context, uint64, string, Kind) (string, error)
}

// BlobStore must keep objects private and publish Put atomically after consuming
// exactly metadata.Size bytes and verifying metadata.SHA256. An error must not
// expose a partial object. Keys are opaque and must not be interpreted as paths.
// Implementations must honor context and must not attach provider credentials.
type BlobStore interface {
	Put(context.Context, string, io.Reader, Metadata) error
	Open(context.Context, string) (io.ReadCloser, error)
	Delete(context.Context, string) error
}

// Download is a bounded, validated temporary file. Close always removes it.
type Download struct {
	Body     io.ReadCloser
	Metadata Metadata
}

type Content struct {
	Body         io.ReadCloser
	Metadata     Metadata
	CacheControl string
}
