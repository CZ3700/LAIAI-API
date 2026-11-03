package video

import (
	"context"
	"errors"
	"time"

	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/task"
	"github.com/laiai/laiai-next/core/internal/watermark"
	"gorm.io/gorm"
)

// Watermark integration. The pipeline is deferred: the trusted-succeeded
// transaction only registers a job; the download+encode runs outside any
// delivery path, and the user-facing video address stays a single field that
// transparently points at the processed copy once one is ready.

// watermarkRegisterTx is called from the observation-apply transactions. It
// registers nothing unless the superadmin toggle is on and the task actually
// delivered a trusted, ready video payload.
func (s *Service) watermarkRegisterTx(tx *gorm.DB, t *task.Task, st *State, now time.Time) error {
	if s.Watermark == nil || t == nil || st == nil {
		return nil
	}
	if t.Status != "succeeded" || st.ResultPayloadRef == "" || st.ArtifactStatus != "ready" {
		return nil
	}
	return watermark.RegisterTx(tx, t.OrgID, t.UserID, t.TaskID, now)
}

// watermarkDelivery swaps the delivered video address for the processed copy
// when one is ready. The response shape is untouched — same single field.
func (s *Service) watermarkDelivery(org uint64, userID *uint64, taskID string, view *TaskView) bool {
	if s.Watermark == nil || view == nil {
		return false
	}
	if url, ok := s.Watermark.DeliveryURL(org, userID, taskID, s.Now().UTC()); ok {
		view.ContentURL = url
		view.ContentSource = "processed"
		if view.Content == nil {
			view.Content = &invocation.VideoDelivery{}
		}
		view.Content.VideoURL = url
		return true
	}
	return false
}

// A processed copy can remain available after the short-lived upstream URL
// capsule expires. Recheck the live task and its tenant retention boundary
// before serving it without the upstream capsule.
func (s *Service) watermarkFallback(ctx context.Context, t task.Task, view *TaskView) bool {
	if s.Watermark == nil || view == nil {
		return false
	}
	err := s.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		currentTask, st, err := loadTask(tx, t.OrgID, t.TaskID, false)
		if err != nil {
			return err
		}
		if currentTask.Status != "succeeded" || st.DeletedAt != nil || !st.CreatedAt.Add(time.Duration(st.RetentionDays)*24*time.Hour).After(s.Now().UTC()) {
			return watermark.ErrNotFound
		}
		return requireTaskRuntimeTx(tx, st)
	})
	if err != nil || !s.watermarkDelivery(t.OrgID, t.UserID, t.TaskID, view) {
		return false
	}
	view.ArtifactStatus = "ready"
	return true
}

// ResolveWatermarkSource produces a fresh upstream signed URL for the worker.
// Evidence signatures expire, so the worker resolves at download time instead
// of trusting anything stored in the job row.
func (s *Service) ResolveWatermarkSource(ctx context.Context, org uint64, taskID string) (string, time.Time, error) {
	if s.Watermark == nil {
		return "", time.Time{}, watermark.ErrNotFound
	}
	t, st, err := loadTask(s.DB.WithContext(ctx), org, taskID, false)
	if err != nil {
		return "", time.Time{}, err
	}
	deadline := st.CreatedAt.Add(time.Duration(st.RetentionDays) * 24 * time.Hour)
	if !deadline.After(s.Now().UTC()) {
		return "", time.Time{}, watermark.ErrNotFound
	}
	links, err := s.readDelivery(ctx, t, st, "watermark-worker")
	if err != nil {
		return "", time.Time{}, err
	}
	if links == nil || links.VideoURL == "" {
		return "", time.Time{}, errors.New("watermark source unavailable")
	}
	return links.VideoURL, deadline, nil
}
