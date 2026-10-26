package video

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"gorm.io/gorm"
)

func TestAuthorityQuerySQLFailureDoesNotLog(t *testing.T) {
	var output bytes.Buffer
	db := authorityClockUnitDB(t, &output)
	// A real failing local query proves the caller logger is active. The alias
	// below only reaches the query logging path, not MySQL time semantics.
	const privateValue = "private-authority-query-fixture"
	query := func(handle *gorm.DB) error {
		var count int64
		return handle.Table("missing_authority_fixture").Where("private_column = ?", privateValue).Count(&count).Error
	}
	if err := query(db); err == nil || !strings.Contains(output.String(), privateValue) {
		t.Fatal("failing SQL logger positive control was not exercised")
	}
	output.Reset()
	s := Service{DB: authorityClockUnitMySQLName(db)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q := s.authorityQuery(ctx)
	if q.Statement.Context != ctx {
		t.Fatal("authority query changed the caller context")
	}
	if err := query(q); err == nil || output.Len() != 0 {
		t.Fatal("authority SQL failure was hidden or logged private parameters")
	}
	cancel()
	if err := query(s.authorityQuery(ctx)); !errors.Is(err, context.Canceled) || output.Len() != 0 {
		t.Fatal("authority query lost caller cancellation or logged SQL")
	}
	// The local suppression must not mutate the shared logger configuration.
	if err := query(db); err == nil || !strings.Contains(output.String(), privateValue) {
		t.Fatal("authority query modified the caller logger")
	}
	output.Reset()
	if err := query((&Service{DB: db}).authorityQuery(context.Background())); !errors.Is(err, ErrUnavailable) || output.Len() != 0 {
		t.Fatal("unsupported dialect executed or logged an authority query")
	}
}
