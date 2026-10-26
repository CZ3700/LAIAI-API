package identity

import (
	"bytes"
	"context"
	"errors"
	"log"
	"reflect"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func routingCandidateDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := routingOrganizationDB(t)
	const schema = `CREATE TABLE users(id INTEGER PRIMARY KEY,status INTEGER NOT NULL);
CREATE TABLE org_members(org_id INTEGER,user_id INTEGER);
CREATE TABLE api_keys(id INTEGER PRIMARY KEY,org_id INTEGER,user_id INTEGER,status INTEGER,expires_at DATETIME,model_access_mode TEXT,routing_group_id INTEGER,policy_version INTEGER,model_limits TEXT,scopes TEXT);
CREATE TABLE org_entitlements(org_id INTEGER PRIMARY KEY,feature_images BOOLEAN,feature_video BOOLEAN);
INSERT INTO users VALUES(7,1);
INSERT INTO org_members VALUES(1,7);
INSERT INTO api_keys VALUES(8,1,7,1,NULL,'inherit_group',9,3,'','');`
	if err := db.Exec(schema).Error; err != nil {
		t.Fatal(err)
	}
	return db
}
func TestRoutingCandidateSubjectRealTxProjectionAndMembership(t *testing.T) {
	db := routingCandidateDB(t)
	tx := db.Begin()
	defer tx.Rollback()
	now := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	if err := tx.Exec("INSERT INTO org_entitlements VALUES(1,0,0)").Error; err != nil {
		t.Fatal(err)
	}
	reads := 0
	if err := db.Callback().Query().Before("gorm:query").Register("candidate_subject_reads", func(q *gorm.DB) {
		reads++
		if q.Statement.ConnPool != tx.Statement.ConnPool {
			t.Error("escaped caller tx")
		}
		if _, ok := q.Statement.Clauses["FOR"]; ok {
			t.Error("unexpected row lock")
		}
	}); err != nil {
		t.Fatal(err)
	}
	dirty := tx.Table("wrong_table").Select("password_hash").Where("org_id=999").Limit(0)
	got, err := ReadRoutingCandidateSubjectTx(context.Background(), dirty, 1, 8, now)
	if err != nil || !got.Found || !got.PrincipalActive || !got.KeyActive || got.Mode != "inherit_group" || got.KeyPolicyVersion != 3 || got.FeatureImages || got.FeatureVideo || reads != 3 {
		t.Fatal(got, err, reads)
	}
	if err := tx.Exec("DELETE FROM org_members WHERE user_id=7").Error; err != nil {
		t.Fatal(err)
	}
	got, err = ReadRoutingCandidateSubjectTx(context.Background(), tx, 1, 8, now)
	if err != nil || got.PrincipalActive {
		t.Fatal("missing membership was active", got, err)
	}
	if err := tx.Exec("UPDATE api_keys SET user_id=NULL WHERE id=8").Error; err != nil {
		t.Fatal(err)
	}
	got, err = ReadRoutingCandidateSubjectTx(context.Background(), tx, 1, 8, now)
	if err != nil || !got.PrincipalActive {
		t.Fatal("org key", got, err)
	}
	if err := tx.Exec("DELETE FROM org_entitlements").Error; err != nil {
		t.Fatal(err)
	}
	got, err = ReadRoutingCandidateSubjectTx(context.Background(), tx, 1, 8, now)
	if err != nil || !got.FeatureImages || !got.FeatureVideo {
		t.Fatal("explicit missing-row defaults", got, err)
	}
	if err := tx.Rollback().Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Callback().Query().Remove("candidate_subject_reads"); err != nil {
		t.Fatal(err)
	}
	var count int64
	if err := db.Table("org_entitlements").Count(&count).Error; err != nil || count != 0 {
		t.Fatal("reader committed caller changes", count, err)
	}
}
func TestRoutingCandidateSubjectAbsentVersusFailedAndExpiry(t *testing.T) {
	db := routingCandidateDB(t)
	tx := db.Begin()
	defer tx.Rollback()
	now := time.Now().UTC().Truncate(time.Millisecond)
	got, err := ReadRoutingCandidateSubjectTx(context.Background(), tx, 2, 8, now)
	if err != nil || got.Found {
		t.Fatal("cross-org fact leak", got, err)
	}
	if err := tx.Exec("UPDATE api_keys SET expires_at=? WHERE id=8", now).Error; err != nil {
		t.Fatal(err)
	}
	got, err = ReadRoutingCandidateSubjectTx(context.Background(), tx, 1, 8, now)
	if err != nil || got.KeyExpired {
		t.Fatal("changed existing exact-expiry boundary", got, err)
	}
	got, err = ReadRoutingCandidateSubjectTx(context.Background(), tx, 1, 8, now.Add(time.Millisecond))
	if err != nil || !got.KeyExpired {
		t.Fatal("expiry missed", got, err)
	}
	if err := tx.Exec("DROP TABLE org_entitlements").Error; err != nil {
		t.Fatal(err)
	}
	got, err = ReadRoutingCandidateSubjectTx(context.Background(), tx, 1, 8, now)
	if !errors.Is(err, ErrRoutingCandidateUnavailable) || !reflect.DeepEqual(got, RoutingCandidateSubject{}) {
		t.Fatal("DB failure became default", got, err)
	}
}
func TestRoutingCandidateSubjectBoundsAndLimitedProjection(t *testing.T) {
	for _, column := range []string{"model_limits", "scopes"} {
		t.Run(column, func(t *testing.T) {
			db := routingCandidateDB(t)
			tx := db.Begin()
			defer tx.Rollback()
			value := strings.Repeat("x", 32768)
			if err := tx.Table("api_keys").Where("id=8").Update(column, value).Error; err != nil {
				t.Fatal(err)
			}
			if _, err := ReadRoutingCandidateSubjectTx(context.Background(), tx, 1, 8, time.Now()); err != nil {
				t.Fatal("exact field limit", err)
			}
			if err := tx.Table("api_keys").Where("id=8").Update(column, value+"x").Error; err != nil {
				t.Fatal(err)
			}
			var statement string
			if err := db.Callback().Query().After("gorm:query").Register("candidate_projection", func(q *gorm.DB) {
				if q.Statement.Table == "api_keys" {
					statement = q.Statement.SQL.String()
				}
			}); err != nil {
				t.Fatal(err)
			}
			got, err := ReadRoutingCandidateSubjectTx(context.Background(), tx, 1, 8, time.Now())
			if !errors.Is(err, ErrRoutingCandidateLimit) || got.Found || !strings.Contains(statement, "CASE WHEN") || strings.Contains(statement, "key_hash") {
				t.Fatal("unbounded or unsafe field projection", err, statement)
			}
		})
	}
}
func TestRoutingCandidateSubjectInvalidAndSafeDeadline(t *testing.T) {
	db := routingCandidateDB(t)
	tx := db.Begin()
	defer tx.Rollback()
	now := time.Now()
	reads := 0
	if err := db.Callback().Query().Before("gorm:query").Register("candidate_subject_count", func(*gorm.DB) { reads++ }); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []*gorm.DB{nil, {}, db, tx.Session(&gorm.Session{DryRun: true})} {
		if _, err := ReadRoutingCandidateSubjectTx(context.Background(), bad, 1, 8, now); !errors.Is(err, ErrRoutingCandidateInvalid) {
			t.Fatal(err)
		}
	}
	if _, err := ReadRoutingCandidateSubjectTx(nil, tx, 1, 8, now); !errors.Is(err, ErrRoutingCandidateInvalid) {
		t.Fatal(err)
	}
	if _, err := ReadRoutingCandidateSubjectTx(context.Background(), tx, 0, 8, now); !errors.Is(err, ErrRoutingCandidateInvalid) {
		t.Fatal(err)
	}
	if reads != 0 {
		t.Fatal("invalid access executed SQL")
	}
	if err := db.Callback().Query().Before("gorm:query").Register("candidate_subject_deadline", func(q *gorm.DB) { <-q.Statement.Context.Done() }); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_, err := ReadRoutingCandidateSubjectTx(context.Background(), tx, 1, 8, now)
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > time.Second {
		t.Fatal(err, time.Since(start))
	}
}
func TestRoutingCandidateSubjectNoSQLDiagnosticLeak(t *testing.T) {
	db := routingCandidateDB(t)
	var logs bytes.Buffer
	db = db.Session(&gorm.Session{Logger: logger.New(log.New(&logs, "", 0), logger.Config{LogLevel: logger.Warn})})
	tx := db.Begin()
	defer tx.Rollback()
	marker := "synthetic-private-subject-diagnostic"
	if err := db.Callback().Query().After("gorm:query").Register("candidate_subject_error", func(q *gorm.DB) { q.AddError(errors.New(marker)) }); err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	_ = tx.Table("api_keys").Find(&rows).Error
	if !strings.Contains(logs.String(), marker) {
		t.Fatal("missing logger positive control")
	}
	logs.Reset()
	_, err := ReadRoutingCandidateSubjectTx(context.Background(), tx, 1, 8, time.Now())
	if !errors.Is(err, ErrRoutingCandidateUnavailable) || strings.Contains(err.Error(), marker) || logs.Len() != 0 {
		t.Fatal(err, logs.String())
	}
}

func TestRoutingCandidateMissingRowAfterSelectDeadlineIsNotDefault(t *testing.T) {
	for _, table := range []string{"api_keys", "org_entitlements"} {
		t.Run(table, func(t *testing.T) {
			db := routingCandidateDB(t)
			if table == "api_keys" {
				if err := db.Exec("DELETE FROM api_keys").Error; err != nil {
					t.Fatal(err)
				}
			}
			tx := db.Begin()
			defer tx.Rollback()
			if err := db.Callback().Query().After("gorm:query").Register("candidate_missing_deadline", func(q *gorm.DB) {
				if q.Statement.Table == table {
					<-q.Statement.Context.Done()
				}
			}); err != nil {
				t.Fatal(err)
			}
			got, err := ReadRoutingCandidateSubjectTx(context.Background(), tx, 1, 8, time.Now())
			if !errors.Is(err, context.DeadlineExceeded) || !reflect.DeepEqual(got, RoutingCandidateSubject{}) {
				t.Fatal("timeout became absent/default facts", got, err)
			}
		})
	}
}

func TestRoutingCandidateMissingKeyAfterCallerCancellationIsNotAbsent(t *testing.T) {
	db := routingCandidateDB(t)
	if err := db.Exec("DELETE FROM api_keys").Error; err != nil {
		t.Fatal(err)
	}
	tx := db.Begin()
	defer tx.Rollback()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := db.Callback().Query().After("gorm:query").Register("candidate_missing_key_cancel", func(*gorm.DB) { cancel() }); err != nil {
		t.Fatal(err)
	}
	got, err := ReadRoutingCandidateSubjectTx(ctx, tx, 1, 8, time.Now())
	if !errors.Is(err, context.Canceled) || !reflect.DeepEqual(got, RoutingCandidateSubject{}) {
		t.Fatal(got, err)
	}
}
