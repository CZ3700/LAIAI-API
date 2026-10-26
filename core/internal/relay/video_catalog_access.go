package relay

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/shopspring/decimal"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var (
	ErrVideoCatalogInvalid     = errors.New("relay: invalid video catalog read")
	ErrVideoCatalogUnavailable = errors.New("relay: video catalog facts unavailable")
)

// VideoCatalogChannel contains metadata observations, never a selected key,
// decrypted credential or a promise that an upstream request will succeed.
type VideoCatalogChannel struct {
	ID                                                               uint64
	PlatformOwned, Enabled, OfficialArkEndpoint, HasUsableCredential bool
	Provider                                                         string
}

type VideoCatalogRatio struct {
	Model string
	Ratio decimal.Decimal
}

// ReadVideoCatalogChannelsTx uses one SELECT and bounded indexed EXISTS
// predicates. Missing channels are omitted. IDs must be sorted and unique.
func ReadVideoCatalogChannelsTx(ctx context.Context, tx *gorm.DB, ids []uint64) ([]VideoCatalogChannel, error) {
	if err := validateVideoCatalogRead(ctx, tx); err != nil {
		return nil, err
	}
	if len(ids) > 4 {
		return nil, ErrVideoCatalogInvalid
	}
	for i, id := range ids {
		if id == 0 || i > 0 && id <= ids[i-1] {
			return nil, ErrVideoCatalogInvalid
		}
	}
	out := make([]VideoCatalogChannel, 0, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	length := func(column string) string { return "OCTET_LENGTH(" + column + ")" }
	if tx.Dialector.Name() == "sqlite" {
		length = func(column string) string { return "LENGTH(CAST(" + column + " AS BLOB))" }
	}
	columns := "c.id, c.owner_org_id, c.status, CASE WHEN " + length("c.provider") + " <= 128 THEN c.provider ELSE NULL END AS provider, CASE WHEN " + length("c.base_url") + " <= 1020 THEN c.base_url ELSE NULL END AS base_url, CASE WHEN EXISTS (SELECT 1 FROM channel_credentials all_credentials WHERE all_credentials.channel_id = c.id) THEN EXISTS (SELECT 1 FROM channel_credentials usable WHERE usable.channel_id = c.id AND usable.status = 1 AND " + length("usable.credential_enc") + " > 0) ELSE CASE WHEN " + length("c.credential_enc") + " > 0 THEN 1 ELSE 0 END END AS has_usable_credential"
	q, cancel, err := videoCatalogSelect(ctx, tx, columns)
	if err != nil {
		return nil, err
	}
	var rows []struct {
		ID, OwnerOrgID      uint64
		Status              int8
		Provider, BaseURL   *string
		HasUsableCredential bool
	}
	err = q.Table("channels AS c").Where("c.id IN ?", ids).Order("c.id ASC").Limit(5).Find(&rows).Error
	if contextErr := q.Statement.Context.Err(); contextErr != nil {
		err = contextErr
	}
	if err != nil {
		safe := videoCatalogReadFailure(q.Statement.Context, err)
		cancel()
		return nil, safe
	}
	cancel()
	if len(rows) > len(ids) {
		return nil, ErrVideoCatalogUnavailable
	}
	requested := make(map[uint64]bool, len(ids))
	for _, id := range ids {
		requested[id] = true
	}
	for i, row := range rows {
		if !requested[row.ID] || i > 0 && row.ID <= rows[i-1].ID || row.Provider == nil || row.BaseURL == nil || !utf8.ValidString(*row.Provider) || !utf8.ValidString(*row.BaseURL) || utf8.RuneCountInString(*row.Provider) > 32 || utf8.RuneCountInString(*row.BaseURL) > 255 {
			return nil, ErrVideoCatalogUnavailable
		}
		out = append(out, VideoCatalogChannel{ID: row.ID, PlatformOwned: row.OwnerOrgID == 0, Enabled: row.Status == 1, Provider: *row.Provider, OfficialArkEndpoint: strings.TrimRight(*row.BaseURL, "/") == "https://ark.cn-beijing.volces.com/api/v3", HasUsableCredential: row.HasUsableCredential})
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

// ReadVideoCatalogRatiosTx preserves EffectiveRatio's two-layer positive-value
// fallback, but never turns a database failure into a price of one. Only the
// four fixed official model IDs are allowed; order is preserved, without N+1.
func ReadVideoCatalogRatiosTx(ctx context.Context, tx *gorm.DB, groupName string, models []string) ([]VideoCatalogRatio, error) {
	if err := validateVideoCatalogRead(ctx, tx); err != nil {
		return nil, err
	}
	if len(models) > 4 || !utf8.ValidString(groupName) || utf8.RuneCountInString(groupName) > 64 {
		return nil, ErrVideoCatalogInvalid
	}
	seen := make(map[string]bool, len(models))
	parts := make([]string, len(models))
	args := make([]any, 0, len(models))
	modelParameter := "?"
	if tx.Dialector.Name() == "mysql" {
		// 0027 fixes the indexed model column to utf8mb4_unicode_ci. Match
		// the derived value's collation without casting the indexed column;
		// connection-default 0900 collations must not produce error 1267.
		modelParameter = "CAST(? AS CHAR CHARACTER SET utf8mb4) COLLATE utf8mb4_unicode_ci"
	}
	for i, model := range models {
		switch model {
		case "seedance-2.0", "seedance-2.5", "seedance-2.0-fast", "seedance-2.0-mini":
		default:
			return nil, ErrVideoCatalogInvalid
		}
		if seen[model] {
			return nil, ErrVideoCatalogInvalid
		}
		seen[model] = true
		parts[i] = fmt.Sprintf("SELECT %d AS ordinal, %s AS model", i, modelParameter)
		args = append(args, model)
	}
	out := make([]VideoCatalogRatio, 0, len(models))
	if len(models) == 0 {
		return out, nil
	}
	// Keep the existing indexed equality predicates and validate every returned
	// identity in Go. A unicode_ci/PAD SPACE near-match is corrupt catalog input,
	// not a missing configuration that may silently fall back to a price of one.
	modelGroupEnabled := "1=1"
	if groupName == "" {
		modelGroupEnabled = "1=0" // legacy ModelGroupRatio skips only exact empty.
	}
	q, cancel, err := videoCatalogSelect(ctx, tx, "requested.ordinal, requested.model, gr.name AS base_group, gr.ratio AS base_ratio, mr.group_name AS model_group, mr.model AS ratio_model, mr.ratio AS model_ratio")
	if err != nil {
		return nil, err
	}
	var rows []struct {
		Ordinal               int
		Model                 string
		BaseGroup, ModelGroup *string
		RatioModel            *string
		BaseRatio, ModelRatio *decimal.Decimal
	}
	err = q.Table("("+strings.Join(parts, " UNION ALL ")+") AS requested", args...).
		Joins("LEFT JOIN group_ratios gr ON gr.name = ?", groupName).
		Joins("LEFT JOIN model_group_ratios mr ON mr.group_name = ? AND mr.model = requested.model AND "+modelGroupEnabled, groupName).
		Order("requested.ordinal ASC").Limit(5).Find(&rows).Error
	if contextErr := q.Statement.Context.Err(); contextErr != nil {
		err = contextErr
	}
	if err != nil {
		safe := videoCatalogReadFailure(q.Statement.Context, err)
		cancel()
		return nil, safe
	}
	cancel()
	if len(rows) != len(models) {
		return nil, ErrVideoCatalogUnavailable
	}
	for i, row := range rows {
		if row.Ordinal != i || row.Model != models[i] || row.BaseGroup != nil && (*row.BaseGroup != groupName || row.BaseRatio == nil) || row.ModelGroup != nil && (*row.ModelGroup != groupName || row.RatioModel == nil || *row.RatioModel != row.Model || row.ModelRatio == nil) {
			return nil, ErrVideoCatalogUnavailable
		}
		base, model := decimal.NewFromInt(1), decimal.NewFromInt(1)
		if row.BaseRatio != nil && row.BaseRatio.IsPositive() {
			base = *row.BaseRatio
		}
		if row.ModelRatio != nil && row.ModelRatio.IsPositive() {
			model = *row.ModelRatio
		}
		out = append(out, VideoCatalogRatio{Model: row.Model, Ratio: base.Mul(model)})
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func validateVideoCatalogRead(ctx context.Context, tx *gorm.DB) error {
	if ctx == nil || tx == nil || tx.Config == nil || tx.Statement == nil || tx.Dialector == nil || tx.DryRun {
		return ErrVideoCatalogInvalid
	}
	if _, ok := tx.Statement.ConnPool.(gorm.TxCommitter); !ok {
		return ErrVideoCatalogInvalid
	}
	if dialect := tx.Dialector.Name(); dialect != "mysql" && dialect != "sqlite" {
		return ErrVideoCatalogInvalid
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if tx.Error != nil {
		return videoCatalogReadFailure(ctx, tx.Error)
	}
	return nil
}

func videoCatalogSelect(ctx context.Context, tx *gorm.DB, columns string) (*gorm.DB, context.CancelFunc, error) {
	child, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	if err := child.Err(); err != nil {
		cancel()
		return nil, func() {}, err
	}
	if tx.Dialector.Name() == "mysql" {
		deadline, _ := child.Deadline()
		ms := min(int64(200), time.Until(deadline).Milliseconds())
		if ms < 1 {
			cancel()
			return nil, func() {}, context.DeadlineExceeded
		}
		columns = fmt.Sprintf("/*+ MAX_EXECUTION_TIME(%d) */ %s", ms, columns)
	}
	return tx.Session(&gorm.Session{NewDB: true, Context: child, Logger: logger.Discard}).Select(columns), cancel, nil
}

func videoCatalogReadFailure(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return ErrVideoCatalogUnavailable
}
