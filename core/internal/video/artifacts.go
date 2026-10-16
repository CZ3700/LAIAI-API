package video

import (
	"context"
	"errors"
	"fmt"
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
			return videoartifact.ErrSourceDeferred
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
	candidates := s.artifactCandidates(ctx, now)
	if e := s.DB.WithContext(ctx).Table("(?) AS eligible", candidates).Where("scope_rank = 1").Order("updated_at, task_id, kind").Limit(10).Find(&rows).Error; e != nil {
		return e
	}
	for _, r := range rows {
		_, _ = s.ArtifactWorker.Transfer(ctx, r.OrgID, r.TaskID, r.Kind, r.Attempts > 0)
		if r.Kind == videoartifact.Video {
			if e := s.syncArtifactProjection(ctx, r.OrgID, r.TaskID, s.Now().UTC()); e != nil {
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
		if r.Kind == videoartifact.Video {
			if e := s.syncArtifactProjection(ctx, r.OrgID, r.TaskID, now); e != nil {
				return e
			}
		}
	}
	return nil
}

func (s *Service) artifactCandidates(ctx context.Context, now time.Time) *gorm.DB {
	return s.DB.WithContext(ctx).Table("video_artifacts AS a").Select("a.*, ROW_NUMBER() OVER (PARTITION BY v.provider_scope_id ORDER BY a.updated_at, a.task_id, a.kind) AS scope_rank").Joins("JOIN video_task_states v ON v.org_id = a.org_id AND v.task_id = a.task_id").Where("v.deleted_at IS NULL AND a.deleted_at IS NULL AND a.expires_at > ? AND a.status IN ? AND a.attempts < ? AND (a.lease_until IS NULL OR a.lease_until <= ?) AND (a.attempts = 0 OR a.updated_at < ?)", now, []string{"pending", "failed"}, s.ArtifactWorker.MaxAttempts, now, now.Add(-time.Minute))
}

// Transfer errors describe one attempt, not the current record. Re-read under
// task/state then artifact locks, the same order as registration/deletion, so
// a late busy/error result can never overwrite another worker's Ready state.
func (s *Service) syncArtifactProjection(ctx context.Context, org uint64, id string, now time.Time) error {
	return s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		_, _, e := loadTask(tx, org, id, true)
		if isNotFound(e) {
			return nil
		}
		if e != nil {
			return e
		}
		var current videoartifact.Record
		if e = locked(tx).Where("org_id = ? AND task_id = ? AND kind = ?", org, id, videoartifact.Video).First(&current).Error; e != nil {
			return e
		}
		if current.DeletedAt != nil {
			return nil
		}
		status := string(current.Status)
		if !current.ExpiresAt.After(now) {
			status = "expired"
		}
		return tx.Model(&State{}).Where("org_id = ? AND task_id = ? AND deleted_at IS NULL", org, id).Updates(map[string]any{"artifact_status": status, "updated_at": now}).Error
	})
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
	c.Header("Content-Length", fmt.Sprint(content.Metadata.Size))
	c.Status(200)
	if n, e := io.Copy(c.Writer, content.Body); e != nil || n != content.Metadata.Size {
		c.Set("video_stream_incomplete", true)
		c.Set("video_error", "content_stream_interrupted")
		s.Log.Warn("video content interrupted", "task_id", c.Param("id"))
	}
}
