package relay

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

var (
	ErrRoutingPublishedInvalid     = errors.New("relay: invalid published channel read")
	ErrRoutingPublishedFacts       = errors.New("relay: invalid published channel facts")
	ErrRoutingPublishedLimit       = errors.New("relay: published channel limit exceeded")
	ErrRoutingPublishedUnavailable = errors.New("relay: published channels unavailable")
)

type RoutingPublishedChannel struct {
	ID, OwnerOrgID uint64
	Enabled        bool
	Models         []string
}

func routingPublishedModel(value string) bool {
	if value == "" || len(value) > 512 || !utf8.ValidString(value) || utf8.RuneCountInString(value) > 128 {
		return false
	}
	for _, c := range value {
		if unicode.IsControl(c) {
			return false
		}
	}
	return true
}
func routingPublishedFailure(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if errors.Is(err, context.Canceled) {
		return context.Canceled
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return context.DeadlineExceeded
	}
	return ErrRoutingPublishedUnavailable
}

// ReadRoutingPublishedChannelsTx only reads operator-published public names.
// The 1MiB semantic total is checked after bounded projection; temporary DB
// transfer is still bounded by 100 rows * 64KiB, not by 1MiB.
func ReadRoutingPublishedChannelsTx(ctx context.Context, tx *gorm.DB, ids []uint64) ([]RoutingPublishedChannel, error) {
	if ctx == nil || tx == nil || tx.Config == nil || tx.Statement == nil || tx.Dialector == nil || tx.DryRun || len(ids) > 100 {
		return nil, ErrRoutingPublishedInvalid
	}
	if _, ok := tx.Statement.ConnPool.(gorm.TxCommitter); !ok {
		return nil, ErrRoutingPublishedInvalid
	}
	if name := tx.Dialector.Name(); name != "mysql" && name != "sqlite" {
		return nil, ErrRoutingPublishedInvalid
	}
	for i, id := range ids {
		if id == 0 || i > 0 && id <= ids[i-1] {
			return nil, ErrRoutingPublishedInvalid
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if tx.Error != nil {
		return nil, routingPublishedFailure(ctx, tx.Error)
	}
	out := make([]RoutingPublishedChannel, 0, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	queryCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	length := "OCTET_LENGTH(models)"
	if tx.Dialector.Name() == "sqlite" {
		length = "LENGTH(CAST(models AS BLOB))"
	}
	columns := "id, owner_org_id, status, " + length + " AS models_bytes, CASE WHEN " + length + " <= 65536 THEN models ELSE NULL END AS models"
	if tx.Dialector.Name() == "mysql" {
		deadline, _ := queryCtx.Deadline()
		ms := time.Until(deadline).Milliseconds()
		if ms < 1 {
			return nil, context.DeadlineExceeded
		}
		columns = fmt.Sprintf("/*+ MAX_EXECUTION_TIME(%d) */ %s", min(ms, int64(200)), columns)
	}
	var rows []struct {
		ID, OwnerOrgID uint64
		Status         int8
		ModelsBytes    *int64
		Models         *string
	}
	err := tx.Session(&gorm.Session{NewDB: true, Context: queryCtx, Logger: logger.Discard}).Table("channels").Select(columns).Where("id IN ?", ids).Order("id ASC").Find(&rows).Error
	if contextErr := queryCtx.Err(); contextErr != nil {
		err = contextErr
	}
	cancel()
	if err != nil {
		return nil, routingPublishedFailure(ctx, err)
	}
	// The 200ms child bounds SQL only. Parsing retains the caller's common
	// resolver deadline instead of spending the expired SELECT allowance.
	var total int64
	for _, row := range rows {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if row.ModelsBytes == nil || *row.ModelsBytes < 0 {
			return nil, ErrRoutingPublishedFacts
		}
		if *row.ModelsBytes > 65536 {
			return nil, ErrRoutingPublishedLimit
		}
		total += *row.ModelsBytes
		if total > 1<<20 {
			return nil, ErrRoutingPublishedLimit
		}
		if row.Models == nil || !utf8.ValidString(*row.Models) {
			return nil, ErrRoutingPublishedFacts
		}
		models := make(map[string]bool)
		for _, value := range strings.Split(*row.Models, ",") {
			value = strings.TrimSpace(value)
			if value == "" {
				continue
			}
			if !routingPublishedModel(value) {
				return nil, ErrRoutingPublishedFacts
			}
			models[value] = true
		}
		sorted := make([]string, 0, len(models))
		for value := range models {
			sorted = append(sorted, value)
		}
		sort.Strings(sorted)
		out = append(out, RoutingPublishedChannel{ID: row.ID, OwnerOrgID: row.OwnerOrgID, Enabled: row.Status == 1, Models: sorted})
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
