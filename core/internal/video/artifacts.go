package video

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/laiai/laiai-next/core/internal/ark"
	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/laiai/laiai-next/core/internal/videoartifact"
	"gorm.io/gorm"
)

func (s *Service) afterObservationTx(tx *gorm.DB, t *task.Task, st *State, hasVideo, hasLastFrame bool, now time.Time) error {
	if t.Status == "succeeded" {
		repo := videoartifact.Repository{DB: s.DB}
		for _, output := range []struct {
			kind    videoartifact.Kind
			present bool
		}{{videoartifact.Video, hasVideo}, {videoartifact.LastFrame, hasLastFrame}} {
			if !output.present {
				continue
			}
			if _, e := repo.RegisterTx(tx, videoartifact.Spec{OrgID: t.OrgID, TaskID: t.TaskID, Kind: output.kind, ExpiresAt: now.Add(time.Duration(st.RetentionDays) * 24 * time.Hour)}, now); e != nil {
				return e
			}
		}
	}
	if terminal(t.Status) && s.Assets != nil {
		return s.Assets.ReleaseBindingsTx(tx, t.OrgID, t.TaskID)
	}
	return nil
}
func (s *Service) afterDeletionTx(tx *gorm.DB, t *task.Task, st *State, now time.Time) error {
	repo := videoartifact.Repository{DB: s.DB}
	if e := repo.HideTaskTx(tx, t.OrgID, t.TaskID, now); e != nil {
		return e
	}
	if s.Assets != nil {
		return s.Assets.ReleaseBindingsTx(tx, t.OrgID, t.TaskID)
	}
	return nil
}

// ResolveSource only returns a signed URL to the private transfer worker. It
// authenticates to the pinned account and keeps every upstream query audited.
func (s *Service) ResolveSource(ctx context.Context, org uint64, id string, kind videoartifact.Kind) (string, error) {
	if kind != videoartifact.Video && kind != videoartifact.LastFrame {
		return "", ErrInvalid
	}
	t, st, e := loadTask(s.DB.WithContext(ctx), org, id, false)
	if e != nil {
		return "", e
	}
	if t.Status != "succeeded" {
		return "", ErrState
	}
	var registered videoartifact.Record
	if e = s.DB.WithContext(ctx).Where("org_id = ? AND task_id = ? AND kind = ? AND deleted_at IS NULL", org, id, kind).First(&registered).Error; e != nil {
		return "", e
	}
	attempt, e := randomID("att_")
	if e != nil {
		return "", e
	}
	now := s.Now().UTC()
	e = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		allowed, e := s.rateTx(tx, st.ProviderScopeID, "query", now)
		if e != nil {
			return e
		}
		if !allowed {
			return ErrUnavailable
		}
		return tx.Create(&Attempt{OrgID: org, TaskID: id, AttemptID: attempt, RequestID: attempt, Phase: "artifact_query_sending", ObservedAt: now}).Error
	})
	if e != nil {
		return "", e
	}
	client, e := s.clientFor(ctx, t, st)
	if e != nil {
		return "", e
	}
	response := client.Query(ctx, t.UpstreamTaskID)
	body := response.Body
	if len(body) == 0 {
		body = []byte(`{}`)
	}
	prepared, e := s.Evidence.Prepare(invocation.VideoPayloadInput{OrgID: org, RequestID: attempt, AttemptID: attempt, Direction: invocation.VideoUpstreamResponse, Purpose: invocation.VideoPurposeHistory, SchemaVersion: 1, Body: body, ExpiresAt: now.Add(time.Duration(st.RetentionDays) * 24 * time.Hour), CaptureEnabled: st.CaptureHistory, SecretValues: []string{client.Credential}})
	if e != nil {
		return "", e
	}
	e = s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		ref, e := s.Evidence.StageTx(tx, prepared)
		if e != nil {
			return e
		}
		return tx.Create(&Attempt{OrgID: org, TaskID: id, AttemptID: attempt, RequestID: attempt, Phase: "artifact_query_response", HTTPStatus: response.HTTPStatus, UpstreamRequestID: response.RequestID, EvidenceRef: ref, ObservedAt: now}).Error
	})
	if e != nil {
		return "", e
	}
	if response.Outcome != ark.OutcomeAccepted || response.Observation == nil || response.Observation.TaskID != t.UpstreamTaskID || response.Observation.Status != ark.StatusSucceeded {
		return "", ErrUnavailable
	}
	if response.Observation.Model != "" && response.Observation.Model != st.UpstreamModel {
		return "", ErrUnavailable
	}
	if kind == videoartifact.LastFrame {
		if response.Observation.LastFrameURL == "" {
			return "", ErrState
		}
		return response.Observation.LastFrameURL, nil
	}
	if response.Observation.VideoURL == "" {
		return "", ErrState
	}
	return response.Observation.VideoURL, nil
}

func (s *Service) ProcessArtifacts(ctx context.Context) error {
	if s.ArtifactWorker == nil || s.ArtifactService == nil {
		return nil
	}
	now := s.Now().UTC()
	var rows []videoartifact.Record
	if e := s.DB.WithContext(ctx).Where("deleted_at IS NULL AND expires_at > ? AND status IN ? AND attempts < ? AND (attempts = 0 OR updated_at < ?)", now, []string{"pending", "failed"}, s.ArtifactWorker.MaxAttempts, now.Add(-time.Minute)).Order("updated_at").Limit(10).Find(&rows).Error; e != nil {
		return e
	}
	for _, r := range rows {
		record, e := s.ArtifactWorker.Transfer(ctx, r.OrgID, r.TaskID, r.Kind, r.Attempts > 0)
		if r.Kind == videoartifact.Video {
			status := "failed"
			if e == nil && record.Status == videoartifact.Ready {
				status = "ready"
			}
			if e := s.DB.Model(&State{}).Where("org_id = ? AND task_id = ? AND deleted_at IS NULL", r.OrgID, r.TaskID).Updates(map[string]any{"artifact_status": status, "updated_at": s.Now().UTC()}).Error; e != nil {
				return e
			}
		}
	}
	var stale []videoartifact.Record
	if e := s.DB.WithContext(ctx).Where("cleaned_at IS NULL AND (deleted_at IS NOT NULL OR expires_at <= ?)", now).Limit(20).Find(&stale).Error; e != nil {
		return e
	}
	for _, r := range stale {
		if e := s.ArtifactService.Cleanup(ctx, r.OrgID, r.TaskID, r.Kind, now); e != nil {
			return e
		}
	}
	return nil
}
func (s *Service) HandleContent(c *gin.Context) {
	key := requestKey(c)
	if _, _, e := loadTask(s.DB, key.OrgID, c.Param("id"), false); e != nil {
		respondError(c, e)
		return
	}
	if s.ArtifactService == nil {
		apiError(c, 503, "artifact_storage_unavailable")
		return
	}
	content, e := s.ArtifactService.Open(c.Request.Context(), key.OrgID, c.Param("id"), videoartifact.Kind(c.DefaultQuery("kind", "video")), s.Now().UTC())
	if e != nil {
		var ae *videoartifact.Error
		if errors.As(e, &ae) {
			apiError(c, ae.HTTPStatus, ae.Code)
		} else {
			respondError(c, e)
		}
		return
	}
	defer content.Body.Close()
	c.Header("Cache-Control", "private, no-store")
	c.Header("Content-Type", content.Metadata.MediaType)
	c.Status(200)
	if _, e = io.Copy(c.Writer, content.Body); e != nil {
		s.Log.Warn("video content interrupted", "task_id", c.Param("id"))
	}
}
