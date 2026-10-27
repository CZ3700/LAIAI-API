package videobilling

import (
	"context"
	"errors"
	"unicode/utf8"

	"github.com/laiai/laiai-next/core/internal/videoruntime"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

type InitialSettlementRef struct {
	OrgID, SnapshotID            uint64
	TaskID, BillingSchemaVersion string
}

type InitialSettlementKind string

const (
	InitialSettlementAbsent                   InitialSettlementKind = "absent"
	InitialSettlementUsageSettled             InitialSettlementKind = "usage_settled"
	InitialSettlementProviderNoChargeReleased InitialSettlementKind = "provider_nocharge_released"
	InitialSettlementNotDispatchedReleased    InitialSettlementKind = "not_dispatched_released"
)

// InitialSettlement contains only validated initial-entry facts. It deliberately
// exposes neither a mutable BillingEntry nor a current adjusted billing view.
type InitialSettlement struct {
	valid  bool
	kind   InitialSettlementKind
	tokens int64
}

func (v InitialSettlement) Kind() InitialSettlementKind { return v.kind }

type InitialSettlementExpectation struct {
	Kind             InitialSettlementKind
	CompletionTokens *int64
}

type InitialSettlementRelation string

const (
	InitialSettlementRelationAbsent     InitialSettlementRelation = "absent"
	InitialSettlementRelationCompatible InitialSettlementRelation = "compatible"
	InitialSettlementRelationConflict   InitialSettlementRelation = "conflict"
)

// Compare does not authorize a settlement. A caller must also validate its own
// evidence, locks and phase. Missing usage is never equivalent to explicit zero.
func (v InitialSettlement) Compare(want InitialSettlementExpectation) (InitialSettlementRelation, error) {
	if !v.valid {
		return "", ErrInvalid
	}
	switch want.Kind {
	case InitialSettlementUsageSettled:
		if want.CompletionTokens == nil || *want.CompletionTokens < 0 {
			return "", ErrInvalid
		}
	case InitialSettlementProviderNoChargeReleased:
		if want.CompletionTokens != nil {
			return "", ErrInvalid
		}
	default:
		return "", ErrInvalid
	}
	if v.kind == InitialSettlementAbsent {
		return InitialSettlementRelationAbsent, nil
	}
	if v.kind != want.Kind || (v.kind == InitialSettlementUsageSettled && v.tokens != *want.CompletionTokens) {
		return InitialSettlementRelationConflict, nil
	}
	return InitialSettlementRelationCompatible, nil
}

type initialSettlementReadError struct{ cause error }

func (*initialSettlementReadError) Error() string {
	return "video_billing: initial settlement read unavailable"
}
func (e *initialSettlementReadError) Unwrap() error { return e.cause }

// Keep driver and context causes inspectable without putting either SQL or
// driver text in the public error string. No error here is a financial conflict.
func initialSettlementReadFailure(ctx context.Context, err error) error {
	if contextErr := ctx.Err(); contextErr != nil {
		return &initialSettlementReadError{cause: errors.Join(err, contextErr)}
	}
	// Only GORM's exact absence sentinel proves absence. A wrapper or mixed
	// failure that also matches it must not turn a failed read into permission.
	if err == gorm.ErrRecordNotFound {
		return ErrNotFound
	}
	if err != nil {
		return &initialSettlementReadError{cause: err}
	}
	return nil
}

type initialSnapshotIdentity struct {
	ID, OrgID, APIKeyID, HoldID  uint64
	TaskID, BillingSchemaVersion string
}

func (s initialSnapshotIdentity) validate(ref InitialSettlementRef) error {
	if s.ID != ref.SnapshotID || s.OrgID != ref.OrgID || s.TaskID != ref.TaskID || s.APIKeyID == 0 || s.HoldID == 0 {
		return ErrInvariant
	}
	if s.BillingSchemaVersion != ref.BillingSchemaVersion {
		return videoruntime.ErrUnsupported
	}
	return nil
}

// ReadInitialSettlementTx keeps the caller's transaction and lock order:
// immutable snapshot identity -> Key -> current Snapshot -> current initial.
// The first read may establish an old RR snapshot; all decisions about absence
// or the winner use the final locking read. It performs at most four reads and
// never looks at video projections, adjusted amounts, quota, or journal rows.
func ReadInitialSettlementTx(tx *gorm.DB, ref InitialSettlementRef) (InitialSettlement, error) {
	if err := requireTx(tx); err != nil {
		return InitialSettlement{}, err
	}
	if tx.Config == nil || tx.Dialector == nil || tx.Statement.Context == nil || tx.DryRun ||
		ref.OrgID == 0 || ref.SnapshotID == 0 || !validID(ref.TaskID) || !utf8.ValidString(ref.TaskID) {
		return InitialSettlement{}, ErrInvalid
	}
	if name := tx.Dialector.Name(); name != "mysql" && name != "sqlite" {
		return InitialSettlement{}, ErrInvalid
	}
	if err := videoruntime.ValidateBillingSchema(ref.BillingSchemaVersion); err != nil {
		return InitialSettlement{}, err
	}
	ctx := tx.Statement.Context
	if err := initialSettlementReadFailure(ctx, tx.Error); err != nil {
		return InitialSettlement{}, err
	}
	clean := func() *gorm.DB {
		// NewDB and Context must be in the same Session to discard inherited
		// filters/projections without losing the original transaction ConnPool.
		return tx.Session(&gorm.Session{NewDB: true, Context: ctx, Logger: logger.Discard})
	}
	readSnapshot := func(current bool, dest *initialSnapshotIdentity) error {
		q := clean().Table("video_billing_snapshots").Select("id, org_id, api_key_id, task_id, billing_schema_version, hold_id").
			Where("id = ? AND org_id = ? AND task_id = ?", ref.SnapshotID, ref.OrgID, ref.TaskID)
		if current {
			q = locked(q)
		}
		return initialSettlementReadFailure(ctx, q.Take(dest).Error)
	}
	var identity initialSnapshotIdentity
	if err := readSnapshot(false, &identity); err != nil {
		return InitialSettlement{}, err
	}
	if err := identity.validate(ref); err != nil {
		return InitialSettlement{}, err
	}
	var key keyIdentity
	if err := initialSettlementReadFailure(ctx, locked(clean()).Table("api_keys").Select("id, org_id").
		Where("id = ? AND org_id = ?", identity.APIKeyID, ref.OrgID).Take(&key).Error); err != nil {
		return InitialSettlement{}, err
	}
	if key.ID != identity.APIKeyID || key.OrgID != ref.OrgID {
		return InitialSettlement{}, ErrInvariant
	}
	var current initialSnapshotIdentity
	if err := readSnapshot(true, &current); err != nil {
		return InitialSettlement{}, err
	}
	if err := current.validate(ref); err != nil {
		return InitialSettlement{}, err
	}
	if current != identity {
		return InitialSettlement{}, ErrInvariant
	}
	var entry BillingEntry
	err := locked(clean()).Select("id, org_id, task_id, settlement_key, state, snapshot_id, hold_id, completion_tokens, reason").
		Where("org_id = ? AND task_id = ? AND settlement_key = ?", ref.OrgID, ref.TaskID, "initial").Take(&entry).Error
	if err = initialSettlementReadFailure(ctx, err); err != nil {
		if err == ErrNotFound {
			return InitialSettlement{valid: true, kind: InitialSettlementAbsent}, nil
		}
		return InitialSettlement{}, err
	}
	if entry.ID == 0 || entry.OrgID != ref.OrgID || entry.TaskID != ref.TaskID || entry.SettlementKey != "initial" ||
		entry.SnapshotID != ref.SnapshotID || entry.HoldID != current.HoldID {
		return InitialSettlement{}, ErrInvariant
	}
	value := InitialSettlement{valid: true}
	switch {
	case entry.State == Settled && entry.Reason == "actual_usage_settled" && entry.CompletionTokens != nil && *entry.CompletionTokens >= 0:
		value.kind, value.tokens = InitialSettlementUsageSettled, *entry.CompletionTokens
	case entry.State == Released && entry.CompletionTokens == nil && entry.Reason == string(ReleaseProviderNoCharge):
		value.kind = InitialSettlementProviderNoChargeReleased
	case entry.State == Released && entry.CompletionTokens == nil && entry.Reason == string(ReleaseNotDispatched):
		value.kind = InitialSettlementNotDispatchedReleased
	default:
		return InitialSettlement{}, ErrInvariant
	}
	return value, nil
}
