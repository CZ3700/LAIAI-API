package video

import (
	"context"

	"github.com/laiai/laiai-next/core/internal/task"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Scheduling predicates deliberately have no SQLite or host-clock fallback.
func (s *Service) authorityQuery(ctx context.Context) *gorm.DB {
	db := s.DB.Session(&gorm.Session{Context: ctx, Logger: logger.Discard})
	if db.Dialector == nil || db.Dialector.Name() != "mysql" {
		db.AddError(ErrUnavailable)
	}
	return db
}

// queryMaySend is the final permission observation after credential loading.
// It does not serialize MySQL with HTTP, and must never be reused to reject
// evidence from a GET that already ran. Terminal tasks awaiting usage remain
// eligible: their existing query sequence, not their status, owns this lease.
func (s *Service) queryMaySend(ctx context.Context, t task.Task, captured State) (bool, error) {
	if err := requireStateRuntime(captured); err != nil {
		return false, err
	}
	if t.OrgID != captured.OrgID || t.TaskID != captured.TaskID || t.UpstreamTaskID == "" || captured.QuerySequence == 0 {
		return false, ErrConflict
	}
	var count int64
	q := supportedTaskRuntime(s.authorityQuery(ctx).Table("video_task_states AS v").
		Joins("JOIN tasks t ON t.org_id = v.org_id AND t.task_id = v.task_id").
		Joins("JOIN video_credential_snapshots c ON c.id = v.credential_snapshot_id AND c.org_id = v.org_id AND c.task_id = v.task_id AND c.provider_scope_id = v.provider_scope_id").
		Joins("JOIN video_upstream_bindings ub ON ub.provider_scope_id = v.provider_scope_id AND ub.upstream_task_id = t.upstream_task_id AND ub.org_id = v.org_id AND ub.task_id = v.task_id")).
		Where(noCreateInboxReview).
		Where("v.org_id = ? AND v.task_id = ? AND OCTET_LENGTH(v.task_id) = ? AND v.query_sequence = ? AND v.query_lease_until > UTC_TIMESTAMP(3) AND v.deleted_at IS NULL", captured.OrgID, captured.TaskID, len(captured.TaskID), captured.QuerySequence).
		Where("t.provider = ? AND t.channel_id = ? AND t.upstream_task_id = ? AND OCTET_LENGTH(t.upstream_task_id) = ? AND ub.upstream_task_id = ? AND OCTET_LENGTH(ub.upstream_task_id) = ?", Provider, t.ChannelID, t.UpstreamTaskID, len(t.UpstreamTaskID), t.UpstreamTaskID, len(t.UpstreamTaskID)).
		Where("OCTET_LENGTH(t.task_id) = ? AND OCTET_LENGTH(c.task_id) = ? AND OCTET_LENGTH(ub.task_id) = ?", len(t.TaskID), len(t.TaskID), len(t.TaskID)).
		Where("v.provider_scope_id = ? AND v.credential_snapshot_id = ? AND v.billing_snapshot_id = ?", captured.ProviderScopeID, captured.CredentialSnapshotID, captured.BillingSnapshotID).
		Where("v.runtime_version = ? AND v.adapter_version = ? AND v.protocol_contract_version = ? AND v.billing_schema_version = ?", captured.RuntimeVersion, captured.AdapterVersion, captured.ProtocolContractVersion, captured.BillingSchemaVersion)
	// These older columns use PAD SPACE collations. Equality plus byte length
	// preserves the captured identity even for a trailing-space mutation.
	for _, field := range []struct{ column, value string }{
		{"v.provider_base_url", captured.ProviderBaseURL},
		{"v.upstream_model", captured.UpstreamModel},
		{"v.config_version", captured.ConfigVersion},
		{"v.capability_version", captured.CapabilityVersion},
	} {
		q = q.Where(field.column+" = ? AND OCTET_LENGTH("+field.column+") = ?", field.value, len(field.value))
	}
	if err := q.Count(&count).Error; err != nil {
		return false, authorityClockFailure(err)
	}
	return count == 1, nil
}
