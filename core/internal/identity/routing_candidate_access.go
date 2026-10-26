package identity

import (
	"context"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var (
	ErrRoutingCandidateInvalid     = errors.New("identity: invalid candidate read")
	ErrRoutingCandidateFacts       = errors.New("identity: invalid candidate facts")
	ErrRoutingCandidateLimit       = errors.New("identity: candidate facts limit exceeded")
	ErrRoutingCandidateUnavailable = errors.New("identity: candidate facts unavailable")
)

// RoutingCandidateSubject contains facts, not successful API-key authentication.
// Reading a grouped row here does not change VerifyKey or key issuance.
type RoutingCandidateSubject struct {
	Found                                  bool
	OrgID, KeyID                           uint64
	KeyActive, KeyExpired, PrincipalActive bool
	Mode                                   string
	RoutingGroupID                         *uint64
	KeyPolicyVersion                       uint64
	ModelLimits, KeyScopes                 string
	FeatureImages, FeatureVideo            bool
}

func routingCandidateFailure(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return ErrRoutingCandidateUnavailable
}
func routingCandidateSelect(ctx context.Context, tx *gorm.DB, columns string, args ...any) (*gorm.DB, context.CancelFunc, error) {
	child, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	if err := child.Err(); err != nil {
		cancel()
		return nil, func() {}, err
	}
	if tx.Dialector.Name() == "mysql" {
		deadline, _ := child.Deadline()
		ms := time.Until(deadline).Milliseconds()
		if ms < 1 {
			cancel()
			return nil, func() {}, context.DeadlineExceeded
		}
		columns = fmt.Sprintf("/*+ MAX_EXECUTION_TIME(%d) */ %s", min(ms, int64(200)), columns)
	}
	return tx.Session(&gorm.Session{NewDB: true, Context: child, Logger: logger.Discard}).Select(columns, args...), cancel, nil
}

func ReadRoutingCandidateSubjectTx(ctx context.Context, tx *gorm.DB, orgID, keyID uint64, at time.Time) (RoutingCandidateSubject, error) {
	var out RoutingCandidateSubject
	if ctx == nil || tx == nil || tx.Config == nil || tx.Statement == nil || tx.Dialector == nil || tx.DryRun || orgID == 0 || keyID == 0 || at.IsZero() {
		return out, ErrRoutingCandidateInvalid
	}
	if _, ok := tx.Statement.ConnPool.(gorm.TxCommitter); !ok {
		return out, ErrRoutingCandidateInvalid
	}
	if name := tx.Dialector.Name(); name != "mysql" && name != "sqlite" {
		return out, ErrRoutingCandidateInvalid
	}
	if err := ctx.Err(); err != nil {
		return out, err
	}
	if tx.Error != nil {
		return out, routingCandidateFailure(ctx, tx.Error)
	}
	length := func(column string) string { return "OCTET_LENGTH(" + column + ")" }
	if tx.Dialector.Name() == "sqlite" {
		length = func(column string) string { return "LENGTH(CAST(" + column + " AS BLOB))" }
	}
	// Both lengths and bounded values are selected together. Oversized stored
	// TEXT never enters a Go string, and NULL cannot become an empty whitelist.
	columns := "id, org_id, user_id, status, expires_at, model_access_mode, routing_group_id, policy_version, " + length("model_limits") + " AS model_limits_bytes, " + length("scopes") + " AS scopes_bytes, CASE WHEN " + length("model_limits") + " <= 32768 THEN model_limits ELSE NULL END AS model_limits, CASE WHEN " + length("scopes") + " <= 32768 THEN scopes ELSE NULL END AS scopes"
	q, cancel, err := routingCandidateSelect(ctx, tx, columns)
	if err != nil {
		return out, err
	}
	var row struct {
		ID, OrgID                     uint64
		UserID                        *uint64
		Status                        int8
		ExpiresAt                     *time.Time
		ModelAccessMode               string
		RoutingGroupID                *uint64
		PolicyVersion                 uint64
		ModelLimitsBytes, ScopesBytes *int64
		ModelLimits, Scopes           *string
	}
	err = q.Table("api_keys").Where("id = ? AND org_id = ?", keyID, orgID).Take(&row).Error
	if contextErr := q.Statement.Context.Err(); contextErr != nil {
		err = contextErr
	}
	if err != nil {
		safe := routingCandidateFailure(q.Statement.Context, err)
		cancel()
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return out, nil
		}
		return out, safe
	}
	cancel()
	if row.ModelLimitsBytes == nil || row.ScopesBytes == nil {
		return out, ErrRoutingCandidateFacts
	}
	if *row.ModelLimitsBytes > 32768 || *row.ScopesBytes > 32768 {
		return out, ErrRoutingCandidateLimit
	}
	if row.ModelLimits == nil || row.Scopes == nil || !utf8.ValidString(*row.ModelLimits) || !utf8.ValidString(*row.Scopes) {
		return out, ErrRoutingCandidateFacts
	}
	if row.ID != keyID || row.OrgID != orgID {
		return out, ErrRoutingCandidateFacts
	}
	out = RoutingCandidateSubject{Found: true, OrgID: row.OrgID, KeyID: row.ID, KeyActive: row.Status == StatusActive, KeyExpired: row.ExpiresAt != nil && at.After(*row.ExpiresAt), Mode: row.ModelAccessMode, RoutingGroupID: row.RoutingGroupID, KeyPolicyVersion: row.PolicyVersion, ModelLimits: *row.ModelLimits, KeyScopes: *row.Scopes}
	columns = "CASE WHEN o.status = 1 THEN 1 ELSE 0 END AS active"
	var args []any
	if row.UserID != nil {
		columns = "CASE WHEN o.status = 1 AND EXISTS (SELECT 1 FROM org_members m JOIN users u ON u.id = m.user_id WHERE m.org_id = o.id AND m.user_id = ? AND u.status = 1) THEN 1 ELSE 0 END AS active"
		args = []any{*row.UserID}
	}
	q, cancel, err = routingCandidateSelect(ctx, tx, columns, args...)
	if err != nil {
		return RoutingCandidateSubject{}, err
	}
	var principal struct{ Active bool }
	err = q.Table("organizations AS o").Where("o.id = ?", orgID).Take(&principal).Error
	if contextErr := q.Statement.Context.Err(); contextErr != nil {
		err = contextErr
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		safe := routingCandidateFailure(q.Statement.Context, err)
		cancel()
		return RoutingCandidateSubject{}, safe
	}
	cancel()
	out.PrincipalActive = err == nil && principal.Active
	q, cancel, err = routingCandidateSelect(ctx, tx, "org_id, feature_images, feature_video")
	if err != nil {
		return RoutingCandidateSubject{}, err
	}
	var entitlement Entitlements
	err = q.Where("org_id = ?", orgID).Take(&entitlement).Error
	if contextErr := q.Statement.Context.Err(); contextErr != nil {
		err = contextErr
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		safe := routingCandidateFailure(q.Statement.Context, err)
		cancel()
		return RoutingCandidateSubject{}, safe
	}
	cancel()
	if errors.Is(err, gorm.ErrRecordNotFound) {
		entitlement = DefaultEntitlements(orgID)
	}
	out.FeatureImages, out.FeatureVideo = entitlement.FeatureImages, entitlement.FeatureVideo
	if err := ctx.Err(); err != nil {
		return RoutingCandidateSubject{}, err
	}
	return out, nil
}
