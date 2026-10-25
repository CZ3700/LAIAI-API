package video

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/laiai/laiai-next/core/internal/videoartifact"
	"github.com/laiai/laiai-next/core/internal/videoruntime"
	"gorm.io/gorm"
)

// Generated media stays with the provider. Even an accidentally installed
// legacy worker cannot re-enable downloads through these hooks.
func (s *Service) afterObservationTx(*gorm.DB, *task.Task, *State, bool, bool, time.Time) error {
	return nil
}
func (s *Service) ProcessArtifacts(context.Context) error { return nil }

func (s *Service) afterDeletionTx(tx *gorm.DB, t *task.Task, st *State, now time.Time) error {
	// Hide old metadata without reading or deleting legacy media files.
	repo := videoartifact.Repository{DB: s.DB}
	if e := repo.HideTaskTx(tx, t.OrgID, t.TaskID, now); e != nil {
		return e
	}
	if s.Assets != nil {
		return s.Assets.ReleaseBindingsTx(tx, t.OrgID, t.TaskID)
	}
	return nil
}

func (s *Service) prepareDelivery(org uint64, attempt string, st State, o *ark.Observation, credential string, now time.Time) (*invocation.PreparedVideoPayload, error) {
	descriptor, e := videoruntime.Resolve(stateRuntime(st))
	if e != nil {
		return nil, e
	}
	if o == nil || o.Status != ark.StatusSucceeded || (o.VideoURL == "" && o.LastFrameURL == "") {
		return nil, nil
	}
	body, e := json.Marshal(invocation.VideoDelivery{VideoURL: o.VideoURL, LastFrameURL: o.LastFrameURL})
	if e != nil {
		return nil, e
	}
	expires := now.Add(24 * time.Hour)
	boundary := st.CreatedAt.Add(time.Duration(st.RetentionDays) * 24 * time.Hour)
	if boundary.Before(expires) {
		expires = boundary
	}
	if !expires.After(now) {
		return nil, nil
	}
	p, e := s.Evidence.Prepare(invocation.VideoPayloadInput{OrgID: org, RequestID: attempt, AttemptID: attempt, Direction: invocation.VideoUpstreamResponse, Purpose: invocation.VideoPurposeDelivery, SchemaVersion: descriptor.DeliverySchemaVersion, Body: body, ExpiresAt: expires, CaptureEnabled: true, SecretValues: []string{credential}})
	// An unusable link must not block settlement of confirmed generation usage.
	if errors.Is(e, invocation.ErrVideoInvalid) {
		return nil, nil
	}
	return p, e
}

func (s *Service) readDelivery(ctx context.Context, t task.Task, st State, actor string) (*invocation.VideoDelivery, error) {
	descriptor, e := videoruntime.Resolve(stateRuntime(st))
	if e != nil {
		return nil, e
	}
	if t.Status != "succeeded" || st.DeletedAt != nil || st.ResultPayloadRef == "" {
		return nil, invocation.ErrVideoNotFound
	}
	checkCurrent := func(tx *gorm.DB) error {
		_, current, e := loadTask(tx, t.OrgID, t.TaskID, false)
		if e != nil {
			return e
		}
		if e = requireTaskRuntimeTx(tx, current); e != nil {
			return e
		}
		if stateRuntime(current) != stateRuntime(st) {
			return videoruntime.ErrUnsupported
		}
		return nil
	}
	if e = s.DB.WithContext(ctx).Transaction(checkCurrent); e != nil {
		return nil, e
	}
	body, e := s.Evidence.ReadForDeliverySchema(ctx, t.OrgID, st.ResultPayloadRef, descriptor.DeliverySchemaVersion, invocation.VideoAccess{ActorRole: invocation.VideoRoleOrgReader, ActorID: actor, ReasonCode: "task_delivery"})
	if e != nil {
		return nil, e
	}
	defer clear(body)
	var links invocation.VideoDelivery
	if e = json.Unmarshal(body, &links); e != nil {
		return nil, invocation.ErrVideoCorrupt
	}
	for _, link := range []string{links.VideoURL, links.LastFrameURL} {
		if link != "" && !invocation.ValidVideoDeliveryURL(link, nil) {
			return nil, invocation.ErrVideoCorrupt
		}
	}
	// A slow encrypted-store read may overlap task deletion. Never release the
	// link if the live tenant-scoped resource disappeared in the meantime.
	e = s.DB.WithContext(ctx).Transaction(checkCurrent)
	if e != nil {
		return nil, e
	}
	return &links, nil
}

// refreshDelivery performs only a rate-limited official GET for one owned task.
// It cannot create a task, change accounting, or access legacy media bytes.
func (s *Service) refreshDelivery(ctx context.Context, t task.Task, st State) error {
	if e := requireStateRuntime(st); e != nil {
		return e
	}
	if t.Status != "succeeded" || t.UpstreamTaskID == "" || st.DeletedAt != nil {
		return ErrState
	}
	now := s.Now().UTC()
	if !st.CreatedAt.Add(time.Duration(st.RetentionDays) * 24 * time.Hour).After(now) {
		return invocation.ErrVideoExpired
	}
	attempt, e := randomID("att_")
	if e != nil {
		return e
	}
	e = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		_, current, e := loadTask(tx, t.OrgID, t.TaskID, true)
		if e != nil {
			return e
		}
		if e = requireTaskRuntimeTx(tx, current); e != nil {
			return e
		}
		if stateRuntime(current) != stateRuntime(st) {
			return videoruntime.ErrUnsupported
		}
		if current.ResultPayloadRef != st.ResultPayloadRef {
			return ErrConflict
		}
		allowed, e := s.rateTx(tx, st.ProviderScopeID, "query", now)
		if e != nil {
			return e
		}
		if !allowed {
			return videoartifact.ErrSourceDeferred
		}
		return tx.Create(&Attempt{OrgID: t.OrgID, TaskID: t.TaskID, AttemptID: attempt, RequestID: attempt, Phase: "delivery_query_sending", ObservedAt: now}).Error
	})
	if e != nil {
		return e
	}
	client, e := s.clientFor(ctx, t, st)
	if e != nil {
		return e
	}
	// Credential loading may overlap a version change. Recheck the live
	// contract before HTTP without treating a refusal as a task outcome.
	e = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		_, current, e := loadTask(tx, t.OrgID, t.TaskID, true)
		if e != nil {
			return e
		}
		if e = requireTaskRuntimeTx(tx, current); e != nil {
			return e
		}
		if stateRuntime(current) != stateRuntime(st) {
			return videoruntime.ErrUnsupported
		}
		if current.ResultPayloadRef != st.ResultPayloadRef {
			return ErrConflict
		}
		return nil
	})
	if e != nil {
		return e
	}
	callStarted := time.Now()
	response := client.Query(ctx, t.UpstreamTaskID)
	s.observeAdapterCall("delivery_query", response, callStarted)
	body := response.Body
	if len(body) == 0 {
		body = []byte(`{}`)
	}
	history, e := s.Evidence.Prepare(invocation.VideoPayloadInput{OrgID: t.OrgID, RequestID: attempt, AttemptID: attempt, Direction: invocation.VideoUpstreamResponse, Purpose: invocation.VideoPurposeHistory, SchemaVersion: 1, Body: body, ExpiresAt: now.Add(time.Duration(st.RetentionDays) * 24 * time.Hour), CaptureEnabled: st.CaptureHistory, SecretValues: []string{client.Credential}})
	if e != nil {
		return e
	}
	var delivery *invocation.PreparedVideoPayload
	o := response.Observation
	if response.Outcome == ark.OutcomeAccepted && o != nil && o.TaskID == t.UpstreamTaskID && o.Status == ark.StatusSucceeded && (o.Model == "" || o.Model == st.UpstreamModel) {
		delivery, e = s.prepareDelivery(t.OrgID, attempt, st, o, client.Credential, s.Now().UTC())
		if e != nil {
			return e
		}
	}
	var applyErr error
	e = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		// A deletion or a newer capsule can win while the HTTP call runs.
		_, current, e := loadTask(tx, t.OrgID, t.TaskID, true)
		if e != nil {
			return e
		}
		ref, e := s.Evidence.StageTx(tx, history)
		if e != nil {
			return e
		}
		if e = tx.Create(&Attempt{OrgID: t.OrgID, TaskID: t.TaskID, AttemptID: attempt, RequestID: attempt, Phase: "delivery_query_response", HTTPStatus: response.HTTPStatus, UpstreamRequestID: response.RequestID, EvidenceRef: ref, ObservedAt: s.Now().UTC()}).Error; e != nil {
			return e
		}
		// Preserve the response produced under the captured contract even if
		// the live task can no longer be interpreted by this binary. Only the
		// delivery projection is withheld; this is never a refund condition.
		if applyErr = requireTaskRuntimeTx(tx, current); applyErr != nil {
			return nil
		}
		if stateRuntime(current) != stateRuntime(st) {
			applyErr = videoruntime.ErrUnsupported
			return nil
		}
		if delivery == nil || current.ResultPayloadRef != st.ResultPayloadRef {
			return nil
		}
		ref, e = s.Evidence.StageTx(tx, delivery)
		if e != nil {
			return e
		}
		status := "pending"
		if o.VideoURL != "" {
			status = "ready"
		}
		return tx.Model(&State{}).Where("org_id = ? AND task_id = ? AND deleted_at IS NULL", t.OrgID, t.TaskID).Updates(map[string]any{"result_payload_ref": ref, "artifact_status": status, "updated_at": s.Now().UTC()}).Error
	})
	if e != nil {
		return e
	}
	if applyErr != nil {
		return applyErr
	}
	if delivery == nil {
		return ErrUnavailable
	}
	return nil
}

func (s *Service) deliveryView(ctx context.Context, t task.Task, st State, actor string, refresh bool) (TaskView, error) {
	v := taskView(t, st)
	if t.Status != "succeeded" || st.DeletedAt != nil {
		return v, nil
	}
	links, e := s.readDelivery(ctx, t, st, actor)
	unsupported := func(err error) bool {
		return errors.Is(err, videoruntime.ErrUnsupported) || errors.Is(err, invocation.ErrVideoSchemaUnsupported)
	}
	if unsupported(e) {
		v.ArtifactStatus = "runtime_contract_unsupported"
		return v, nil
	}
	missing := func(err error) bool {
		return errors.Is(err, invocation.ErrVideoNotFound) || errors.Is(err, invocation.ErrVideoAccessDenied) || errors.Is(err, invocation.ErrVideoExpired) || errors.Is(err, invocation.ErrVideoRetentionDenied)
	}
	if e != nil && !missing(e) {
		return TaskView{}, e
	}
	if e != nil && refresh {
		if refreshErr := s.refreshDelivery(ctx, t, st); refreshErr == nil {
			freshTask, freshState, err := loadTask(s.DB.WithContext(ctx), t.OrgID, t.TaskID, false)
			if err != nil {
				return TaskView{}, err
			}
			links, e = s.readDelivery(ctx, freshTask, freshState, actor)
			if unsupported(e) {
				v.ArtifactStatus = "runtime_contract_unsupported"
				return v, nil
			}
			if e != nil && !missing(e) {
				return TaskView{}, e
			}
		} else if unsupported(refreshErr) {
			v.ArtifactStatus = "runtime_contract_unsupported"
			return v, nil
		} else if errors.Is(refreshErr, invocation.ErrVideoExpired) {
			e = refreshErr
		} else if !errors.Is(refreshErr, videoartifact.ErrSourceDeferred) && !errors.Is(refreshErr, ErrConflict) && !errors.Is(refreshErr, ErrState) {
			return TaskView{}, refreshErr
		}
	}
	if e != nil {
		v.ArtifactStatus = "pending"
		if errors.Is(e, invocation.ErrVideoExpired) {
			v.ArtifactStatus = "expired"
		}
		return v, nil
	}
	v.Content = links
	v.ContentURL = links.VideoURL
	if links.VideoURL != "" {
		v.ArtifactStatus = "ready"
	} else {
		v.ArtifactStatus = "pending"
	}
	return v, nil
}

func (s *Service) HandleContent(c *gin.Context) {
	key := requestKey(c)
	kind := c.DefaultQuery("kind", "video")
	if kind != "video" && kind != "last_frame" {
		respondError(c, ErrInvalid)
		return
	}
	t, st, e := loadTask(s.DB.WithContext(c.Request.Context()), key.OrgID, c.Param("id"), false)
	if e != nil {
		respondError(c, e)
		return
	}
	v, e := s.deliveryView(c.Request.Context(), t, st, fmt.Sprint(key.ID), true)
	if e != nil {
		respondError(c, e)
		return
	}
	link := v.ContentURL
	if kind == "last_frame" {
		link = ""
		if v.Content != nil {
			link = v.Content.LastFrameURL
		}
	}
	if link == "" {
		if v.ArtifactStatus == "runtime_contract_unsupported" {
			apiError(c, 409, "runtime_contract_unsupported")
		} else if v.ArtifactStatus == "expired" {
			apiError(c, 410, "content_expired")
		} else {
			apiError(c, 409, "content_not_ready")
		}
		return
	}
	c.Header("Cache-Control", "private, no-store")
	c.JSON(200, gin.H{"url": link, "kind": kind, "source": "upstream"})
}
