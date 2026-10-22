package videoasset

import (
	"context"
	"errors"

	"gorm.io/gorm"
)

// LatestSyncJob restores the current organization's owned sync progress after a
// page reload. A changed default route must not resurrect a previous scope's job.
func (s *Service) LatestSyncJob(ctx context.Context, org uint64, kind string) (*SyncJob, error) {
	if org == 0 || (kind != KindVirtual && kind != "real_person") {
		return nil, ErrInvalid
	}
	r, err := s.resolve(ctx, org, Scope{})
	if err != nil {
		return nil, err
	}
	var job SyncJob
	err = owned(ctx, s.db.WithContext(ctx), org, "sync", "id").
		Where("org_id = ? AND scope_id = ? AND scope_version = ? AND account_id = ? AND region = ? AND project = ? AND mode = ? AND kind = ?",
			org, r.Scope.ID, r.Scope.Version, r.Scope.AccountID, r.Scope.Region, r.Scope.Project, "owned", kind).
		Order("created_at DESC, id DESC").Take(&job).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, ErrStorage
	}
	// Some supported databases use case-insensitive text collations. Do not
	// expose a job whose immutable identity differs despite a SQL equality match.
	if job.OrgID != org || job.Mode != "owned" || job.Kind != kind ||
		!sameScope(Scope{job.ScopeID, job.ScopeVersion, job.AccountID, job.Region, job.Project}, r.Scope) {
		return nil, ErrStorage
	}
	return &job, nil
}
