package videoasset

import (
	"time"

	"github.com/laiai/laiai-next/core/internal/invocation"
)

// History is optional and uses the organization's current retention policy.
// Required execution requests and sync checkpoints have separate bounded
// lifetimes and must never be routed through this history-only helper.
func (s *Service) prepareHistoryEvidence(r RuntimeScope, input invocation.VideoPayloadInput) (*invocation.PreparedVideoPayload, error) {
	if r.RetentionDays < 1 || r.RetentionDays > 30 {
		return nil, ErrExecutionPolicy
	}
	input.Purpose = invocation.VideoPurposeHistory
	input.CaptureEnabled = r.CaptureHistory
	input.ExpiresAt = nowMillis(s).Add(time.Duration(r.RetentionDays) * 24 * time.Hour)
	return s.evidence.Prepare(input)
}
