package video

import (
	"errors"
	"time"

	"github.com/laiai/laiai-next/core/internal/ark"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrProviderModelConcurrencyLimit = errors.New("video: provider account model concurrency limit reached")

type ProviderModelSendGate struct {
	AccountRef      string     `gorm:"primaryKey;column:account_ref"`
	FoundationModel string     `gorm:"primaryKey;column:foundation_model"`
	NextAllowedAt   time.Time  `gorm:"column:next_allowed_at"`
	NextAllowedAt4K *time.Time `gorm:"column:next_allowed_at_4k"`
	UpdatedAt       time.Time  `gorm:"column:updated_at"`
}

func (ProviderModelSendGate) TableName() string { return "video_provider_model_send_gates" }

// foundationModelForTask only accepts a configured public model and its
// reviewed upstream deployment grammar. Removing the six-digit deployment
// suffix produces the account-wide foundation model used by Ark's rate API.
// A caller-supplied arbitrary model string cannot create a quota pool.
func foundationModelForTask(publicModel, upstreamModel string) (string, error) {
	if !ark.ValidUpstreamModel(publicModel, upstreamModel) || len(upstreamModel) < 8 {
		return "", ErrCapability
	}
	foundation := upstreamModel[:len(upstreamModel)-7]
	if !foundationModelPattern.MatchString(foundation) {
		return "", ErrCapability
	}
	return foundation, nil
}

// readProviderModelQuotaTx is called inside the existing admission or durable
// send transaction. A missing row means the old scope gate remains in force;
// it does not create a fictitious official allocation. All scopes with the
// same main account and foundation read the same quota row.
func readProviderModelQuotaTx(tx *gorm.DB, accountRef, publicModel, upstreamModel string) (ProviderModelQuota, string, bool, error) {
	var q ProviderModelQuota
	foundation, err := foundationModelForTask(publicModel, upstreamModel)
	if err != nil || !validProviderQuotaKey(accountRef, foundation) || tx == nil {
		return q, "", false, ErrCapability
	}
	err = locked(tx).Where("account_ref = ? AND foundation_model = ?", accountRef, foundation).Take(&q).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return q, foundation, false, nil
	}
	if err != nil {
		return q, foundation, false, err
	}
	return q, foundation, true, nil
}

func quotaPace(rpm int) time.Duration {
	intervalMS := (int64(time.Minute/time.Millisecond) + int64(rpm) - 1) / int64(rpm)
	if intervalMS < 1 {
		intervalMS = 1
	}
	return time.Duration(intervalMS) * time.Millisecond
}

// Seedance 2.5 Premium uses independent 4K and non-4K allocations. Until
// account-specific 4K data is present, keep the documented 1 concurrent / 15 RPM
// ceiling; tighter official or manual limits, including zero, still win.
func seedance25FourKQuota(q ProviderModelQuotaEffective) ProviderModelQuotaEffective {
	rpm, concurrent := 15, 1
	if q.CreateTaskRPM4K != nil && *q.CreateTaskRPM4K < rpm {
		rpm = *q.CreateTaskRPM4K
	}
	if q.ConcurrentRequests4K != nil && *q.ConcurrentRequests4K < concurrent {
		concurrent = *q.ConcurrentRequests4K
	}
	q.CreateTaskRPM, q.ConcurrentRequests = nil, nil
	q.CreateTaskRPM4K, q.ConcurrentRequests4K = &rpm, &concurrent
	return q
}

// checkProviderModelConcurrencyTx counts tasks for the main account and
// reviewed public model across organizations, channels, projects and scopes.
// Admission includes accepted work; outbound dispatch excludes accepted work
// so an admitted backlog can drain when the provider is at capacity.
// 2.0 4K consumes the general pool and its subpool; 2.5 uses independent pools.
func (s *Service) checkProviderModelConcurrencyTx(tx *gorm.DB, accountRef, publicModel, upstreamModel string, is4K, includeAccepted bool) error {
	q, _, found, err := readProviderModelQuotaTx(tx, accountRef, publicModel, upstreamModel)
	if err != nil || !found && !(publicModel == ark.Model25 && is4K) {
		return err
	}
	effective := q.Effective()
	if publicModel == ark.Model25 && is4K {
		effective = seedance25FourKQuota(effective)
	}
	// A known zero CreateTask RPM is a hard upstream/manual denial. Reject
	// fresh admissions before reserving funds; already-accepted work is
	// retried by claimProviderModelSendTx if the quota later rises.
	if includeAccepted && (effective.CreateTaskRPM != nil && *effective.CreateTaskRPM == 0 || is4K && effective.CreateTaskRPM4K != nil && *effective.CreateTaskRPM4K == 0) {
		return ErrModelRateLimit
	}
	if effective.ConcurrentRequests == nil && (!is4K || effective.ConcurrentRequests4K == nil) {
		return nil
	}
	active := []string{"submitting", "submission_unknown", "queued", "running"}
	if includeAccepted {
		active = append(active, "accepted")
	}
	base := func() *gorm.DB {
		return locked(tx).Table("video_task_parameters AS p").Joins("JOIN tasks AS t ON t.org_id = p.org_id AND t.task_id = p.task_id").Where("p.account_ref = ? AND p.public_model = ? AND t.provider = ? AND t.status IN ?", accountRef, publicModel, Provider, active)
	}
	resolutionPath := "JSON_UNQUOTE(JSON_EXTRACT(p.parameters_json, '$.Resolution'))"
	if tx.Dialector.Name() == "sqlite" {
		resolutionPath = "json_extract(p.parameters_json, '$.Resolution')"
	}
	var count int64
	if effective.ConcurrentRequests != nil {
		query := base()
		if publicModel == ark.Model25 {
			query = query.Where(resolutionPath+" <> ?", "4k")
		}
		if err := query.Count(&count).Error; err != nil {
			return err
		}
		if count >= int64(*effective.ConcurrentRequests) {
			return ErrProviderModelConcurrencyLimit
		}
	}
	if is4K && effective.ConcurrentRequests4K != nil {
		count = 0
		if err := base().Where(resolutionPath+" = ?", "4k").Count(&count).Error; err != nil {
			return err
		}
		if count >= int64(*effective.ConcurrentRequests4K) {
			return ErrProviderModelConcurrencyLimit
		}
	}
	return nil
}

// claimProviderModelSendTx spends the account-wide CreateTask allocation for
// the selected pool (independent 4K/non-4K for 2.5). It is independent of the legacy
// scope/action rateTx gate. Denial must leave the task accepted and its hold
// intact; worker candidate selection excludes it until retryAt.
func (s *Service) claimProviderModelSendTx(tx *gorm.DB, accountRef, publicModel, upstreamModel string, is4K bool, at time.Time) (allowed bool, retryAt time.Time, err error) {
	q, foundation, found, err := readProviderModelQuotaTx(tx, accountRef, publicModel, upstreamModel)
	if err != nil {
		return false, time.Time{}, err
	}
	if !found && !(publicModel == ark.Model25 && is4K) {
		return true, time.Time{}, nil
	}
	effective := q.Effective()
	if publicModel == ark.Model25 && is4K {
		effective = seedance25FourKQuota(effective)
	}
	base := effective.CreateTaskRPM
	var fourK *int
	if is4K {
		fourK = effective.CreateTaskRPM4K
	}
	if base == nil && fourK == nil {
		return true, time.Time{}, nil
	}
	now, err := userLimitNowTx(tx, at)
	if err != nil {
		return false, time.Time{}, err
	}
	// Zero is a real upstream/manual denial. Recheck periodically so an
	// accepted backlog resumes after an administrator or Ark raises the cap.
	if base != nil && *base == 0 || fourK != nil && *fourK == 0 {
		return false, now.Add(30 * time.Second), nil
	}
	gate := ProviderModelSendGate{AccountRef: accountRef, FoundationModel: foundation, NextAllowedAt: now, UpdatedAt: now}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&gate).Error; err != nil {
		return false, time.Time{}, err
	}
	if err := locked(tx).Where("account_ref = ? AND foundation_model = ?", accountRef, foundation).Take(&gate).Error; err != nil {
		return false, time.Time{}, err
	}
	now, err = userLimitNowTx(tx, at)
	if err != nil {
		return false, time.Time{}, err
	}
	if base != nil && gate.NextAllowedAt.After(now) {
		retryAt = gate.NextAllowedAt
	}
	if fourK != nil && gate.NextAllowedAt4K != nil && gate.NextAllowedAt4K.After(retryAt) && gate.NextAllowedAt4K.After(now) {
		retryAt = *gate.NextAllowedAt4K
	}
	if !retryAt.IsZero() {
		return false, retryAt, nil
	}
	updates := map[string]any{"updated_at": now}
	if base != nil {
		updates["next_allowed_at"] = now.Add(quotaPace(*base))
	}
	if fourK != nil {
		updates["next_allowed_at_4k"] = now.Add(quotaPace(*fourK))
	}
	result := tx.Model(&ProviderModelSendGate{}).Where("account_ref = ? AND foundation_model = ?", accountRef, foundation).Updates(updates)
	if result.Error != nil || result.RowsAffected != 1 {
		return false, time.Time{}, ErrUnavailable
	}
	return true, time.Time{}, nil
}
