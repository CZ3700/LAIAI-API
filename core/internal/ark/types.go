// Package ark implements the official Ark inference protocol. It does not own
// database transactions, credentials at rest, retries, or billing decisions.
package ark

import "net/http"

const (
	Model20                       = "seedance-2.0"
	Model20Fast                   = "seedance-2.0-fast"
	Model20Mini                   = "seedance-2.0-mini"
	Model25                       = "seedance-2.5"
	DefaultMaxRequestBytes  int64 = 10 << 20
	DefaultMaxResponseBytes int64 = 1 << 20
)

// Error is safe to return or log; Message never contains upstream free text.
type Error struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	Field      string `json:"field,omitempty"`
	Retryable  bool   `json:"retryable"`
	HTTPStatus int    `json:"-"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }
func failure(code, message, field string, status int) *Error {
	return &Error{Code: code, Message: message, Field: field, HTTPStatus: status}
}

type MediaReference struct {
	URL string `json:"url"`
}
type DraftReference struct {
	ID string `json:"id"`
}

// ContentItem is a closed tagged union, checked by Decode and Normalize.
type ContentItem struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ImageURL  *MediaReference `json:"image_url,omitempty"`
	VideoURL  *MediaReference `json:"video_url,omitempty"`
	AudioURL  *MediaReference `json:"audio_url,omitempty"`
	DraftTask *DraftReference `json:"draft_task,omitempty"`
	Role      string          `json:"role,omitempty"`
}
type CreateRequest struct {
	Model                  string        `json:"model"`
	Content                []ContentItem `json:"content"`
	Duration               *int          `json:"duration,omitempty"`
	Frames                 *int          `json:"frames,omitempty"`
	Resolution             *string       `json:"resolution,omitempty"`
	Ratio                  *string       `json:"ratio,omitempty"`
	Seed                   *int64        `json:"seed,omitempty"`
	GenerateAudio          *bool         `json:"generate_audio,omitempty"`
	Watermark              *bool         `json:"watermark,omitempty"`
	ReturnLastFrame        *bool         `json:"return_last_frame,omitempty"`
	Draft                  *bool         `json:"draft,omitempty"`
	ServiceTier            *string       `json:"service_tier,omitempty"`
	OmniReferenceTaskType  *string       `json:"omni_reference_task_type,omitempty"`
	OutputFormat           *string       `json:"output_format,omitempty"`
	ExecutionExpiresAfter  *int          `json:"execution_expires_after,omitempty"`
	MaxChargeUSD           *string       `json:"max_charge_usd,omitempty"`
	ExpectedPricingVersion *string       `json:"expected_pricing_version,omitempty"`
}

// Capability is trusted versioned gateway configuration. No model is enabled
// or verified implicitly. Zero media limits disable those media.
type Capability struct {
	PublicModel, UpstreamModel, Version                                              string
	Enabled, Verified                                                                bool
	Resolutions, Ratios                                                              []string
	MinDuration, MaxDuration, DefaultDuration                                        int
	DefaultResolution, DefaultRatio                                                  string
	DefaultSeed                                                                      int64
	DefaultGenerateAudio, DefaultWatermark                                           bool
	DefaultReturnLastFrame, DefaultDraft                                             bool
	DefaultExecutionExpiresAfter, MinExecutionExpiresAfter, MaxExecutionExpiresAfter int
	AutomaticDuration, Frames                                                        bool
	MinFrames, MaxFrames, FrameStep                                                  int
	FPS                                                                              int
	MaxContentItems, MaxImages, MaxVideos, MaxAudios                                 int
	ImageInput, VideoInput, AudioInput, ImageFrames                                  bool
	GenerateAudio, ReturnLastFrame, Draft, DraftConversion                           bool
	OmniReferenceTaskTypes, OutputFormats                                            []string
}

// EffectiveParameters are billing/observation context, not necessarily fields
// permitted in the outbound JSON (notably inherited Draft parameters).
type EffectiveParameters struct {
	Resolution, Ratio                                string
	Duration, Frames, FramesPerSecond                int
	Seed                                             int64
	GenerateAudio, Watermark, ReturnLastFrame, Draft bool
	ServiceTier, OmniReferenceTaskType, OutputFormat string
	ExecutionExpiresAfter                            int
}
type AssetBinding struct{ MediaType, UpstreamURI string }
type DraftBinding struct {
	UpstreamTaskID                 string
	PublicModel, CapabilityVersion string
	Parameters                     EffectiveParameters
	HasInputVideo                  bool
}

// Bindings must be resolved after gateway checks of Org, provider scope,
// readiness, licence and lifetime. This package cannot authorize references.
type Bindings struct {
	Assets map[string]AssetBinding
	Drafts map[string]DraftBinding
}
type PlatformOptions struct{ MaxChargeUSD, ExpectedPricingVersion *string }
type NormalizedRequest struct {
	PublicModel, UpstreamModel, CapabilityVersion string
	Body                                          []byte `json:"-"`
	Parameters                                    EffectiveParameters
	HasInputVideo                                 bool
	AssetIDs                                      []string
	SourceDraftID                                 string
	Platform                                      PlatformOptions
}

type Status string

const (
	StatusAccepted          Status = "accepted"
	StatusSubmitting        Status = "submitting"
	StatusSubmissionUnknown Status = "submission_unknown"
	StatusQueued            Status = "queued"
	StatusRunning           Status = "running"
	StatusSucceeded         Status = "succeeded"
	StatusFailed            Status = "failed"
	StatusCancelled         Status = "cancelled"
	StatusExpired           Status = "expired"
)

type Outcome string

const (
	OutcomeAccepted Outcome = "accepted"
	OutcomeRejected Outcome = "rejected"
	OutcomeNotSent  Outcome = "not_sent"
	OutcomeUnknown  Outcome = "unknown"
)

type Action string

const (
	ActionSubmit Action = "submit"
	ActionQuery  Action = "query"
	ActionDelete Action = "delete"
)

type Usage struct {
	CompletionTokens *int64 `json:"completion_tokens"`
	TotalTokens      *int64 `json:"total_tokens"`
}

// Observation is internal evidence. Upstream IDs and URLs are not public DTOs.
type Observation struct {
	TaskID, Model                     string
	Status                            Status
	RawStatus                         string `json:"-"`
	Usage                             Usage
	VideoURL, LastFrameURL            string `json:"-"`
	Resolution, Ratio                 string
	Duration, Frames, FramesPerSecond *int64
	Seed                              *int64
	CreatedAt, UpdatedAt              *int64
	ServiceTier                       string
	ExecutionExpiresAfter             *int64
	GenerateAudio                     *bool
	Draft                             *bool
	Priority                          *int64
	HasError                          bool
}

// Result.Body is bounded raw evidence for the encrypted Invocation Store only.
// Successful DELETE is an acknowledgement, never proof of cancellation.
type Result struct {
	Action      Action
	Outcome     Outcome
	HTTPStatus  int
	RequestID   string
	TaskID      string       `json:"-"`
	Observation *Observation `json:"-"`
	Body        []byte       `json:"-"`
	Truncated   bool
	Error       *Error
}
type Client struct {
	BaseURL, Credential string
	HTTPClient          *http.Client
	MaxResponseBytes    int64
}

// Transition is a pure proposal for the gateway's CAS/receipt transaction.
// It grants no permission to release or charge money.
type Transition struct {
	Status                             Status
	Changed, NeedsReview, UsagePending bool
	Error                              *Error
}
type CancelDecision struct {
	OperationStatus                        string
	SendDelete, CancelLocally, NeedsReview bool
	Error                                  *Error
}
