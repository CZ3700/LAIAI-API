package video

import (
	"context"
	"errors"
	"time"

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
	return watermark.RegisterTx(tx, t.OrgID, t.TaskID, now)
}

// watermarkDelivery swaps the delivered video address for the processed copy
// when one is ready. The response shape is untouched — same single field.
func (s *Service) watermarkDelivery(org uint64, taskID string, view *TaskView) {
	if s.Watermark == nil || view == nil || view.ContentURL == "" {
		return
	}
	if url, ok := s.Watermark.DeliveryURL(org, taskID, s.Now().UTC()); ok {
		view.ContentURL = url
	}
}

// ResolveWatermarkSource produces a fresh upstream signed URL for the worker.
// Evidence signatures expire, so the worker resolves at download time instead
// of trusting anything stored in the job row.
func (s *Service) ResolveWatermarkSource(ctx context.Context, org uint64, taskID string) (string, error) {
	if s.Watermark == nil {
		return "", watermark.ErrNotFound
	}
	t, st, err := loadTask(s.DB.WithContext(ctx), org, taskID, false)
	if err != nil {
		return "", err
	}
	links, err := s.readDelivery(ctx, t, st, "watermark-worker")
	if err != nil {
		return "", err
	}
	if links == nil || links.VideoURL == "" {
		return "", errors.New("watermark source unavailable")
	}
	return links.VideoURL, nil
}
