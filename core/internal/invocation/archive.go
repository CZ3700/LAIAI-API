package invocation

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// InvocationArchive stores cold-archived invocation metadata moved out of the hot table.
type InvocationArchive struct {
	ID                uint64          `gorm:"primaryKey;autoIncrement:false;column:id" json:"id"`
	OrgID             uint64          `gorm:"column:org_id" json:"-"`
	UserID            *uint64         `gorm:"column:user_id" json:"user_id,omitempty"`
	APIKeyID          uint64          `gorm:"column:api_key_id" json:"api_key_id"`
	RequestID         string          `gorm:"column:request_id" json:"request_id"`
	Model             string          `gorm:"column:model" json:"model"`
	Modality          string          `gorm:"column:modality" json:"modality,omitempty"`
	ChannelID         uint64          `gorm:"column:channel_id" json:"-"`
	Provider          string          `gorm:"column:provider" json:"-"`
	Status            string          `gorm:"column:status" json:"status"`
	StatusCode        int             `gorm:"column:status_code" json:"status_code"`
	IsStream          bool            `gorm:"column:is_stream" json:"is_stream"`
	Attempts          int             `gorm:"column:attempts" json:"-"`
	PromptTokens      int64           `gorm:"column:prompt_tokens" json:"prompt_tokens"`
	CompletionTokens  int64           `gorm:"column:completion_tokens" json:"completion_tokens"`
	CostUSD           decimal.Decimal `gorm:"column:cost_usd" json:"cost_usd"`
	UsageSource       string          `gorm:"column:usage_source" json:"usage_source,omitempty"`
	FRTMs             int             `gorm:"column:frt_ms" json:"frt_ms"`
	DurationMs        int             `gorm:"column:duration_ms" json:"duration_ms"`
	UpstreamRequestID string          `gorm:"column:upstream_request_id" json:"upstream_request_id,omitempty"`
	ErrorBrief        string          `gorm:"column:error_brief" json:"error_brief,omitempty"`
	PayloadObjectKey  string          `gorm:"column:payload_object_key" json:"-"`
	FinishReason      string          `gorm:"column:finish_reason" json:"finish_reason,omitempty"`
	Protocol          string          `gorm:"column:protocol" json:"protocol,omitempty"`
	CachedTokens      int64           `gorm:"column:cached_tokens" json:"cached_tokens"`
	ReasoningTokens   int64           `gorm:"column:reasoning_tokens" json:"reasoning_tokens"`
	UpstreamErrorCode string          `gorm:"column:upstream_error_code" json:"upstream_error_code,omitempty"`
	ClientIP          string          `gorm:"column:client_ip" json:"client_ip,omitempty"`
	UserAgent         string          `gorm:"column:user_agent" json:"user_agent,omitempty"`
	Referer           string          `gorm:"column:referer" json:"referer,omitempty"`
	ForwardRuleEID    string          `gorm:"column:forward_rule_eid" json:"forward_rule_eid,omitempty"`
	BillingRulePID    string          `gorm:"column:billing_rule_pid" json:"billing_rule_pid,omitempty"`
	UpstreamCostRatio decimal.Decimal `gorm:"column:upstream_cost_ratio" json:"-"`
	CreatedAt         time.Time       `gorm:"column:created_at" json:"created_at"`
	ArchivedAt        time.Time       `gorm:"column:archived_at" json:"archived_at"`
}

func (InvocationArchive) TableName() string { return "invocations_archive" }

func toArchiveRow(inv Invocation, now time.Time) InvocationArchive {
	return InvocationArchive{
		ID:                inv.ID,
		OrgID:             inv.OrgID,
		UserID:            inv.UserID,
		APIKeyID:          inv.APIKeyID,
		RequestID:         inv.RequestID,
		Model:             inv.Model,
		Modality:          inv.Modality,
		ChannelID:         inv.ChannelID,
		Provider:          inv.Provider,
		Status:            inv.Status,
		StatusCode:        inv.StatusCode,
		IsStream:          inv.IsStream,
		Attempts:          inv.Attempts,
		PromptTokens:      inv.PromptTokens,
		CompletionTokens:  inv.CompletionTokens,
		CostUSD:           inv.CostUSD,
		UsageSource:       inv.UsageSource,
		FRTMs:             inv.FRTMs,
		DurationMs:        inv.DurationMs,
		UpstreamRequestID: inv.UpstreamRequestID,
		ErrorBrief:        inv.ErrorBrief,
		PayloadObjectKey:  inv.PayloadObjectKey,
		FinishReason:      inv.FinishReason,
		Protocol:          inv.Protocol,
		CachedTokens:      inv.CachedTokens,
		ReasoningTokens:   inv.ReasoningTokens,
		UpstreamErrorCode: inv.UpstreamErrorCode,
		ClientIP:          inv.ClientIP,
		UserAgent:         inv.UserAgent,
		Referer:           inv.Referer,
		ForwardRuleEID:    inv.ForwardRuleEID,
		BillingRulePID:    inv.BillingRulePID,
		UpstreamCostRatio: inv.UpstreamCostRatio,
		CreatedAt:         inv.CreatedAt,
		ArchivedAt:        now,
	}
}

// WithArchive exposes the same invocation metadata from the hot and archive tables.
func WithArchive(db *gorm.DB) *gorm.DB {
	if db.Dialector.Name() == "sqlite" && !db.Migrator().HasTable(&InvocationArchive{}) {
		return db.Model(&Invocation{})
	}
	return db.Model(&Invocation{}).Table("(SELECT " + invocationColumns + " FROM invocations UNION ALL SELECT " + invocationColumns + " FROM invocations_archive) AS invocations")
}

const invocationColumns = "id, org_id, user_id, api_key_id, request_id, model, modality, channel_id, provider, status, status_code, is_stream, attempts, prompt_tokens, completion_tokens, cost_usd, usage_source, frt_ms, duration_ms, upstream_request_id, error_brief, payload_object_key, finish_reason, protocol, cached_tokens, reasoning_tokens, upstream_error_code, client_ip, user_agent, referer, forward_rule_eid, billing_rule_pid, upstream_cost_ratio, created_at"

// CleanupInvocationContent clears heavy detail text fields on old invocations in small batches
// while preserving token, billing, model, and status metadata.
func CleanupInvocationContent(ctx context.Context, db *gorm.DB, cutoff time.Time, batch int) (int64, error) {
	if db == nil {
		return 0, nil
	}
	if batch <= 0 {
		batch = 5000
	}
	var total int64
	for {
		if ctx.Err() != nil {
			return total, ctx.Err()
		}
		var ids []uint64
		if err := db.WithContext(ctx).Model(&Invocation{}).
			Where("created_at < ? AND (error_brief <> '' OR user_agent <> '' OR referer <> '' OR payload_object_key <> '')", cutoff).
			Order("id ASC").Limit(batch).Pluck("id", &ids).Error; err != nil {
			return total, fmt.Errorf("invocation: query stale detail rows: %w", err)
		}
		if len(ids) == 0 {
			return total, nil
		}
		res := db.WithContext(ctx).Model(&Invocation{}).
			Where("id IN ?", ids).
			Updates(map[string]any{
				"error_brief":        "",
				"user_agent":         "",
				"referer":            "",
				"payload_object_key": "",
			})
		if res.Error != nil {
			return total, fmt.Errorf("invocation: clear detail rows: %w", res.Error)
		}
		total += res.RowsAffected
		if len(ids) < batch {
			return total, nil
		}
		if err := sleepWithContext(ctx, 20*time.Millisecond); err != nil {
			return total, err
		}
	}
}

// CleanupExpiredErrorLogs deletes settled non-200 error records older than cutoff
// from both invocations and invocations_archive.
func CleanupExpiredErrorLogs(ctx context.Context, db *gorm.DB, cutoff time.Time, batch int) (hotDeleted, archiveDeleted int64, err error) {
	if db == nil {
		return 0, 0, nil
	}
	if batch <= 0 {
		batch = 5000
	}
	hotDeleted, err = deleteExpiredErrorsFromTable(ctx, db, "invocations", &Invocation{}, cutoff, batch)
	if err != nil {
		return hotDeleted, 0, err
	}
	archiveDeleted, err = deleteExpiredErrorsFromTable(ctx, db, "invocations_archive", &InvocationArchive{}, cutoff, batch)
	if err != nil {
		if isMissingArchiveTable(err) {
			return hotDeleted, 0, nil
		}
		return hotDeleted, archiveDeleted, err
	}
	return hotDeleted, archiveDeleted, nil
}

func deleteExpiredErrorsFromTable(ctx context.Context, db *gorm.DB, table string, model any, cutoff time.Time, batch int) (int64, error) {
	var total int64
	for _, predicate := range []string{"status_code NOT IN (200, 0)", "status_code = 0 AND status <> 'success'"} {
		for {
			if ctx.Err() != nil {
				return total, ctx.Err()
			}
			var ids []uint64
			if err := db.WithContext(ctx).Table(table).
				Where("created_at < ?", cutoff).Where(predicate).
				Order("created_at ASC, id ASC").Limit(batch).Pluck("id", &ids).Error; err != nil {
				return total, fmt.Errorf("invocation: query expired errors from %s: %w", table, err)
			}
			if len(ids) == 0 {
				break
			}
			res := db.WithContext(ctx).Where("id IN ?", ids).Where(predicate).Delete(model)
			if res.Error != nil {
				return total, fmt.Errorf("invocation: delete expired errors from %s: %w", table, res.Error)
			}
			total += res.RowsAffected
			if len(ids) < batch {
				break
			}
			if err := sleepWithContext(ctx, 20*time.Millisecond); err != nil {
				return total, err
			}
		}
	}
	return total, nil
}

// ArchiveOldInvocations moves cold rows older than (rowDays + 2) days from invocations
// to invocations_archive after syncing daily stats first.
func ArchiveOldInvocations(ctx context.Context, db *gorm.DB, loc *time.Location, now time.Time, rowDays, batch int) (int64, error) {
	if db == nil || rowDays <= 0 {
		return 0, nil
	}
	if batch <= 0 {
		batch = 2000
	}
	if _, err := SyncDailyStatsAt(ctx, db, loc, now, false, nil); err != nil {
		return 0, fmt.Errorf("invocation: pre-archive daily sync: %w", err)
	}
	local := now.In(resolveSyncLoc(loc))
	cutoff := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, local.Location()).AddDate(0, 0, -(rowDays + 2)).UTC()
	var total int64
	for {
		if ctx.Err() != nil {
			return total, ctx.Err()
		}
		var moved int
		err := db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			var rows []Invocation
			if err := tx.Where("created_at < ?", cutoff).
				Order("created_at ASC, id ASC").Limit(batch).Find(&rows).Error; err != nil {
				return err
			}
			if len(rows) == 0 {
				return nil
			}
			nowTS := now.UTC()
			archives := make([]InvocationArchive, 0, len(rows))
			ids := make([]uint64, 0, len(rows))
			for _, r := range rows {
				archives = append(archives, toArchiveRow(r, nowTS))
				ids = append(ids, r.ID)
			}
			if err := tx.Clauses(clause.OnConflict{
				Columns:   []clause.Column{{Name: "id"}},
				DoNothing: true,
			}).Create(&archives).Error; err != nil {
				return err
			}
			if err := tx.Where("id IN ?", ids).Delete(&Invocation{}).Error; err != nil {
				return err
			}
			moved = len(rows)
			return nil
		})
		if err != nil {
			return total, fmt.Errorf("invocation: archive rows batch: %w", err)
		}
		total += int64(moved)
		if moved < batch {
			return total, nil
		}
		if err := sleepWithContext(ctx, 20*time.Millisecond); err != nil {
			return total, err
		}
	}
}

func isMissingArchiveTable(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return errors.Is(err, gorm.ErrRecordNotFound) ||
		strings.Contains(msg, "no such table") ||
		strings.Contains(msg, "does not exist")
}

func sleepWithContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
