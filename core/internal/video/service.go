package video

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/crypto"
	"github.com/laiai/laiai-next/core/internal/identity"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/relay"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/laiai/laiai-next/core/internal/videoartifact"
	"github.com/laiai/laiai-next/core/internal/videoasset"
	"github.com/laiai/laiai-next/core/internal/videobilling"
	"github.com/shopspring/decimal"
	"gorm.io/gorm"
)

type Service struct {
	DB              *gorm.DB
	Keyring         *crypto.Keyring
	Evidence        *invocation.VideoStore
	Secret          []byte
	Client          *http.Client
	Log             *slog.Logger
	Now             func() time.Time
	Gate            task.Gate
	ArtifactWorker  *videoartifact.Worker
	ArtifactService *videoartifact.Service
	Assets          *videoasset.Service
	// Endpoint overrides exist for injected httptest clients only. Production
	// configuration uses the fixed mainland official endpoint.
	AllowTestEndpoint bool
}

func NewService(db *gorm.DB, kr *crypto.Keyring, secret []byte, log *slog.Logger) (*Service, error) {
	if db == nil || kr == nil || len(secret) < 32 {
		return nil, ErrUnavailable
	}
	if log == nil {
		log = slog.Default()
	}
	s := &Service{DB: db, Keyring: kr, Secret: append([]byte(nil), secret...), Client: relay.NewGuardedClient(2*time.Minute, false), Log: log, Now: time.Now}
	store, err := invocation.NewVideoStore(db, kr, invocation.VideoStoreOptions{Now: func() time.Time { return s.Now().UTC() }})
	if err != nil {
		return nil, err
	}
	s.Evidence = store
	return s, nil
}

type Dimensions struct {
	Width  int64 `json:"width"`
	Height int64 `json:"height"`
}
type createPlan struct {
	config     ModelConfig
	scope      ProviderScope
	normalized ark.NormalizedRequest
	quote      videobilling.QuoteResult
	canonical  []byte
	resources  taskResourcePlan
}

func (s *Service) plan(tx *gorm.DB, org uint64, in ark.CreateRequest) (createPlan, error) {
	var p createPlan
	if err := locked(tx).Where("public_model = ? AND enabled = ?", in.Model, true).First(&p.config).Error; err != nil {
		return p, ErrCapability
	}
	if p.config.VerifiedAt == nil || p.config.Version == "" {
		return p, ErrCapability
	}
	if err := tx.Where("id = ? AND enabled = ?", p.config.ProviderScopeID, true).First(&p.scope).Error; err != nil {
		return p, ErrCapability
	}
	if p.scope.Region != "cn-beijing" || p.scope.AccountRef == "" || p.scope.ProjectName == "" {
		return p, ErrCapability
	}
	var cap ark.Capability
	if json.Unmarshal([]byte(p.config.CapabilityJSON), &cap) != nil || cap.PublicModel != in.Model {
		return p, ErrCapability
	}
	effectiveInput, bindings, resources, err := s.taskBindingsTx(tx, org, in, p.scope, cap)
	if err != nil {
		return p, err
	}
	p.resources = resources
	normalized, err := ark.Normalize(effectiveInput, cap, bindings)
	if err != nil {
		return p, err
	}
	p.normalized = normalized
	var dims map[string]Dimensions
	if json.Unmarshal([]byte(p.config.BoundsJSON), &dims) != nil {
		return p, ErrCapability
	}
	dimension, ok := dims[normalized.Parameters.Resolution+"/"+normalized.Parameters.Ratio]
	if !ok || dimension.Width <= 0 || dimension.Height <= 0 {
		return p, ErrCapability
	}
	effective := normalized.Parameters
	duration := int64(effective.Duration) * 1000
	if effective.Duration == -1 {
		duration = int64(cap.MaxDuration) * 1000
	}
	frames := int64(effective.Frames)
	if frames > 0 {
		duration = 0
	}
	inputDuration := int64(0)
	inputSource := "none"
	inputCount := 0
	var inputVideoCount *int = &inputCount
	if normalized.HasInputVideo {
		inputSource = "verified_media"
		for _, item := range effectiveInput.Content {
			if item.Type == "video_url" {
				inputCount++
			}
		}
		inputDuration = resources.InputDurationMS
		if resources.HasUnverifiedVideo {
			inputDuration = p.config.MaxInputVideoDurationMS
			inputSource = "configured_upper_bound"
		}
		if normalized.SourceDraftID != "" {
			inputSource = "draft_inherited"
			inputVideoCount = nil // Older draft metadata did not persist a count.
		}
		if inputDuration <= 0 || p.config.MaxInputVideoDurationMS > 0 && inputDuration > p.config.MaxInputVideoDurationMS {
			return p, ErrCapability
		}
	}
	p.resources.InputDurationMS = inputDuration
	fx, err := decimal.NewFromString(p.scope.FXUSDPerCNY)
	if err != nil {
		return p, ErrCapability
	}
	mode := "generate"
	if effective.OmniReferenceTaskType == "edit" || effective.OmniReferenceTaskType == "extend" {
		mode = effective.OmniReferenceTaskType
	}
	if normalized.SourceDraftID != "" {
		mode = "draft_conversion"
	}
	rule, err := videobilling.ResolvePricingTx(tx, videobilling.PricingKey{ProviderScopeID: p.scope.ID, Model: in.Model, Mode: mode, Resolution: effective.Resolution, InputVideo: normalized.HasInputVideo, ServiceTier: effective.ServiceTier, Draft: effective.Draft}, s.Now().UTC())
	if err != nil {
		return p, err
	}
	if rule.Version != p.config.PricingVersion {
		return p, ErrCapability
	}
	canonical, err := json.Marshal(in)
	if err != nil {
		return p, ErrInvalid
	}
	p.canonical = canonical
	meter := videobilling.MeteringInput{InputVideo: normalized.HasInputVideo, InputDurationMS: inputDuration, OutputDurationMS: duration, OutputFrames: frames, Width: dimension.Width, Height: dimension.Height, FPSNumerator: int64(effective.FramesPerSecond), FPSDenominator: 1, BoundsVerified: cap.Verified, CapabilityVersion: cap.Version, ParentDraftTaskID: normalized.SourceDraftID, EffectiveRatio: relay.EffectiveRatio(tx, org, in.Model), FXUSDPerCNY: fx, FXVersion: p.scope.FXVersion, FXObservedAt: p.scope.FXObservedAt}
	meter.InputDurationSource, meter.InputVideoCount = inputSource, inputVideoCount
	p.quote, err = videobilling.QuoteWithMinimumTx(tx, rule, meter, videobilling.MinimumContext{Resolution: effective.Resolution, Ratio: effective.Ratio, Ratios: cap.Ratios, MinDuration: cap.MinDuration, MaxDuration: cap.MaxDuration, AutomaticDuration: effective.Duration == -1})
	if err != nil {
		return p, err
	}
	if in.ExpectedPricingVersion != nil && *in.ExpectedPricingVersion != rule.Version {
		return p, ErrConflict
	}
	if in.MaxChargeUSD != nil {
		max, e := decimal.NewFromString(*in.MaxChargeUSD)
		if e != nil || max.IsNegative() {
			return p, ErrInvalid
		}
		if p.quote.HoldUSD.GreaterThan(max) {
			return p, ErrConflict
		}
	}
	return p, nil
}

func (s *Service) Estimate(org uint64, in ark.CreateRequest) (videobilling.QuoteResult, error) {
	var p createPlan
	err := s.DB.Transaction(func(tx *gorm.DB) error { var err error; p, err = s.plan(tx, org, in); return err })
	return p.quote, err
}

func (s *Service) Create(ctx context.Context, key *identity.APIKey, requestID, idempotency string, in ark.CreateRequest) (TaskView, bool, error) {
	if key == nil || !validIdempotency(idempotency) {
		return TaskView{}, false, ErrInvalid
	}
	if !key.AllowsModel(in.Model) {
		return TaskView{}, false, ErrForbidden
	}
	canonical, err := json.Marshal(in)
	if err != nil {
		return TaskView{}, false, ErrInvalid
	}
	taskID, err := randomID("task_")
	if err != nil {
		return TaskView{}, false, err
	}
	opID, err := randomID("op_")
	if err != nil {
		return TaskView{}, false, err
	}
	now := s.Now().UTC()
	var result TaskView
	replay := false
	// A first read supports preparation before T1. A replay is still resolved
	// under the idempotency lock before any capability or price revalidation.
	var p createPlan
	var execution, effectiveHistory *invocation.PreparedVideoPayload
	err = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		idem, repeat, e := claimIdempotencyTx(tx, s.Secret, key.OrgID, http.MethodPost, "/v1/video/generations", idempotency, canonical, taskID, opID, now)
		if e != nil {
			return e
		}
		if repeat {
			t, st, e := loadTask(tx, key.OrgID, idem.ResourceID, false)
			if e != nil {
				return e
			}
			result = taskView(t, st)
			replay = true
			return s.linkCreateRequestTx(tx, key.OrgID, requestID, t, st, idem.OperationID, true)
		}
		var policy Policy
		if e = tx.Where("org_id = ? AND async_execution_allowed = ?", key.OrgID, true).First(&policy).Error; e != nil {
			return ErrForbidden
		}
		if policy.RetentionDays < 1 || policy.RetentionDays > 30 {
			return ErrCapability
		}
		p, e = s.plan(tx, key.OrgID, in)
		if e != nil {
			return e
		}
		// Encryption is local CPU work. There is no remote KMS/blob call inside T1.
		execution, e = s.Evidence.Prepare(invocation.VideoPayloadInput{OrgID: key.OrgID, RequestID: requestID, Direction: invocation.VideoEffectiveRequest, Purpose: invocation.VideoPurposeExecution, SchemaVersion: 1, Body: p.normalized.Body, ExpiresAt: now.Add(24 * time.Hour), CaptureEnabled: true})
		if e != nil {
			return e
		}
		effectiveHistory, e = s.Evidence.Prepare(invocation.VideoPayloadInput{OrgID: key.OrgID, RequestID: requestID, Direction: invocation.VideoEffectiveRequest, Purpose: invocation.VideoPurposeHistory, SchemaVersion: 1, Body: p.normalized.Body, ExpiresAt: now.Add(time.Duration(policy.RetentionDays) * 24 * time.Hour), CaptureEnabled: policy.CaptureHistory, CaptureRepresentation: "effective_json_redacted"})
		if e != nil {
			return e
		}
		var ch relay.Channel
		if e = locked(tx).Where("id = ? AND status = ?", p.config.ChannelID, 1).First(&ch).Error; e != nil {
			return ErrUnavailable
		}
		if !s.AllowTestEndpoint && strings.TrimRight(ch.BaseURL, "/") != "https://ark.cn-beijing.volces.com/api/v3" {
			return ErrCapability
		}
		if ch.Provider != "volcengine" && ch.Provider != "modelark" {
			return ErrCapability
		}
		var active int64
		if e = tx.Model(&task.Task{}).Where("channel_id = ? AND provider = ? AND status IN ?", ch.ID, Provider, []string{"accepted", "submitting", "submission_unknown", "queued", "running"}).Count(&active).Error; e != nil {
			return e
		}
		limit := ch.MaxConcurrentTasks
		if limit <= 0 {
			limit = 3
		}
		if active >= int64(limit) {
			return task.ErrConcurrencyLimit
		}
		credentialID, ciphertext, ok := relay.SelectCredential(tx, &ch)
		if !ok {
			return ErrUnavailable
		}
		credential := CredentialSnapshot{OrgID: key.OrgID, TaskID: taskID, ProviderScopeID: p.scope.ID, SourceCredentialID: credentialID, Ciphertext: ciphertext, CreatedAt: now}
		if e = tx.Create(&credential).Error; e != nil {
			return e
		}
		reserved, e := videobilling.ReserveTx(tx, videobilling.ReserveInput{OrgID: key.OrgID, APIKeyID: key.ID, TaskID: taskID, RequestHMAC: idem.BodyHMAC, Quote: p.quote, Now: now})
		if e != nil {
			return e
		}
		payload, e := s.Evidence.StageTx(tx, execution)
		if e != nil {
			return e
		}
		if _, e = s.Evidence.StageTx(tx, effectiveHistory); e != nil {
			return e
		}
		if e = s.ensureClientHistoryTx(tx, key.OrgID, requestID, canonical, policy, now); e != nil {
			return e
		}
		t := task.Task{TaskID: taskID, OrgID: key.OrgID, UserID: key.UserID, APIKeyID: key.ID, Model: in.Model, ChannelID: ch.ID, Provider: Provider, Status: "accepted", HoldID: reserved.Snapshot.HoldID, CredentialID: credentialID, EstTokens: p.quote.EstimatedTokens, PriceOutUSDPer1M: p.quote.Rule.RetailUSDPer1M, HoldAmountUSD: p.quote.HoldUSD, GroupRatio: p.quote.Metering.EffectiveRatio, SubmittedAt: now}
		if e = tx.Create(&t).Error; e != nil {
			return e
		}
		if e = s.persistTaskResourcesTx(tx, key.OrgID, taskID, p); e != nil {
			return e
		}
		st := State{TaskID: taskID, OrgID: key.OrgID, CreateRequestID: requestID, ProviderScopeID: p.scope.ID, CredentialSnapshotID: credential.ID, ProviderBaseURL: ch.BaseURL, ConfigVersion: p.config.Version, CapabilityVersion: p.normalized.CapabilityVersion, UpstreamModel: p.normalized.UpstreamModel, NoChargeStatuses: p.config.NoChargeStatuses, BillingState: "reserved", SyncState: "fresh", ArtifactStatus: "pending", EffectivePayloadRef: payload, BillingSnapshotID: reserved.Snapshot.ID, IdempotencyRecordID: idem.ID, ParentDraftTaskID: p.normalized.SourceDraftID, RowVersion: 1, CreatedAt: now, UpdatedAt: now}
		st.NextPollAt = now
		st.CaptureHistory = policy.CaptureHistory
		st.RetentionDays = policy.RetentionDays
		if e = tx.Create(&st).Error; e != nil {
			return e
		}
		if e = tx.Model(&State{}).Where("org_id = ? AND task_id = ?", key.OrgID, taskID).Updates(map[string]any{"capture_history": policy.CaptureHistory, "retention_days": policy.RetentionDays}).Error; e != nil {
			return e
		}
		op := Operation{ID: opID, OrgID: key.OrgID, ResourceID: taskID, Kind: "create_task", Status: "pending", Version: 1, PayloadRef: payload, CreatedAt: now, UpdatedAt: now}
		if e = tx.Create(&op).Error; e != nil {
			return e
		}
		if e = appendEventTx(tx, &t, &st, "task.accepted", "", "accepted", requestID, payload, now); e != nil {
			return e
		}
		result = taskView(t, st)
		return s.linkCreateRequestTx(tx, key.OrgID, requestID, t, st, opID, false)
	})
	return result, replay, err
}

func (s *Service) Get(org uint64, id string) (TaskView, error) {
	t, st, err := loadTask(s.DB, org, id, false)
	if err != nil {
		return TaskView{}, err
	}
	return taskView(t, st), nil
}

func (s *Service) clientFor(ctx context.Context, t task.Task, st State) (*ark.Client, error) {
	var snapshot CredentialSnapshot
	if err := s.DB.WithContext(ctx).Where("org_id = ? AND task_id = ? AND id = ? AND provider_scope_id = ?", t.OrgID, t.TaskID, st.CredentialSnapshotID, st.ProviderScopeID).First(&snapshot).Error; err != nil {
		return nil, ErrUnavailable
	}
	credential, err := s.Keyring.Decrypt(snapshot.Ciphertext)
	if err != nil {
		return nil, ErrUnavailable
	}
	return &ark.Client{BaseURL: st.ProviderBaseURL, Credential: credential, HTTPClient: s.Client}, nil
}

func safeCode(err error) string {
	switch {
	case errors.Is(err, ErrConflict):
		return "idempotency_conflict"
	case errors.Is(err, ErrState):
		return "invalid_state"
	case errors.Is(err, ErrCapability):
		return "unsupported_capability"
	case errors.Is(err, ErrForbidden):
		return "forbidden"
	}
	return "internal_error"
}

func (s *Service) String() string { return fmt.Sprintf("video.Service(%T)", s.DB) }
