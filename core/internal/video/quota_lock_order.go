package video

import (
	"errors"

	"github.com/laiai/laiai-next/core/internal/task"
	"gorm.io/gorm"
)

// createQuotaIdentity contains only immutable admission metadata. Its initial
// read is intentionally nonlocking: the worker must acquire the user limit
// and main-account/model quota rows before it locks the existing task row.
// Admission uses the same user -> account order before its locking counts.
type createQuotaIdentity struct {
	OrgID           uint64
	TaskID          string
	UserID          *uint64
	AccountRef      string
	PublicModel     string
	UpstreamModel   string
	ProviderScopeID uint64
}

func (id createQuotaIdentity) matchesTaskState(t task.Task, st State) bool {
	if id.OrgID != t.OrgID || id.OrgID != st.OrgID || id.TaskID != t.TaskID || id.TaskID != st.TaskID || id.PublicModel != t.Model || id.UpstreamModel != st.UpstreamModel || id.ProviderScopeID != st.ProviderScopeID {
		return false
	}
	if (id.UserID == nil) != (t.UserID == nil) {
		return false
	}
	return id.UserID == nil || *id.UserID == *t.UserID
}

func (id createQuotaIdentity) matchesParameters(p TaskParameters) bool {
	return id.OrgID == p.OrgID && id.TaskID == p.TaskID && id.AccountRef == p.AccountRef && id.PublicModel == p.PublicModel && id.UpstreamModel == p.UpstreamModel && id.ProviderScopeID == p.ProviderScopeID
}

// prelockCreateQuotaIdentityTx must be the first read in the create claim
// transaction, before loadTask(..., true). A missing limit/quota row still
// receives MySQL's primary-key gap lock under repeatable-read, preventing a
// concurrent administrator INSERT from changing the lock order mid-claim.
// The caller validates the returned identity after loading locked task/state
// and again after loading task parameters; a mismatch aborts before send.
func (s *Service) prelockCreateQuotaIdentityTx(tx *gorm.DB, orgID uint64, taskID string) (createQuotaIdentity, error) {
	var id createQuotaIdentity
	if tx == nil || orgID == 0 || taskID == "" {
		return id, ErrInvalid
	}
	err := tx.Table("tasks AS t").
		Select("t.org_id, t.task_id, t.user_id, t.model AS public_model, p.account_ref, p.upstream_model, p.provider_scope_id").
		Joins("JOIN video_task_parameters AS p ON p.org_id = t.org_id AND p.task_id = t.task_id").
		Where("t.org_id = ? AND t.task_id = ? AND t.provider = ?", orgID, taskID, Provider).
		Take(&id).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return id, ErrNotFound
	}
	if err != nil {
		return id, err
	}
	foundation, mapErr := foundationModelForTask(id.PublicModel, id.UpstreamModel)
	if id.OrgID != orgID || id.TaskID != taskID || mapErr != nil || !validProviderQuotaKey(id.AccountRef, foundation) {
		return id, ErrUnavailable
	}
	if id.UserID != nil {
		if _, _, err := readUserTaskLimitTx(tx, orgID, *id.UserID); err != nil {
			return id, err
		}
	}
	if _, _, _, err := readProviderModelQuotaTx(tx, id.AccountRef, id.PublicModel, id.UpstreamModel); err != nil {
		return id, err
	}
	return id, nil
}
