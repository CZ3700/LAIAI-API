package invocation

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"io"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	drivermysql "github.com/go-sql-driver/mysql"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Each invocation fixture has a private database so a full-table retention job
// cannot erase another test's payloads. Only the tracked invocation migrations
// are applied here; the release gate separately replays every migration.
func videoExecutionMySQLStore(t *testing.T) (*VideoStore, *gorm.DB, *time.Time) {
	return videoExecutionMySQLStoreWithPurgeIndex(t, true)
}

// Only the dedicated 0132 replay test uses the pre-index schema. All lifecycle
// tests execute the real candidate SQL against the tracked current indexes.
func videoExecutionMySQLStoreWithPurgeIndex(t *testing.T, withPurgeIndex bool) (*VideoStore, *gorm.DB, *time.Time) {
	t.Helper()
	dsn := os.Getenv("ARK_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("ARK_TEST_MYSQL_DSN is required for real MySQL invocation tests")
	}
	cfg, err := drivermysql.ParseDSN(dsn)
	if err != nil || cfg.DBName != "ark_test" {
		t.Fatal("invocation tests require the isolated ark_test control database")
	}
	cfg.ParseTime, cfg.Loc, cfg.MultiStatements = true, time.UTC, true
	cfg.Timeout, cfg.ReadTimeout, cfg.WriteTimeout = 5*time.Second, 30*time.Second, 5*time.Second
	cfg.Logger = log.New(io.Discard, "", 0)
	controlConnector, err := drivermysql.NewConnector(cfg)
	if err != nil {
		t.Fatal("open isolated MySQL control connection")
	}
	admin := sql.OpenDB(controlConnector)
	defer admin.Close()
	var token [12]byte
	if _, err := rand.Read(token[:]); err != nil {
		t.Fatal(err)
	}
	name := "ark_execution_" + hex.EncodeToString(token[:])
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE `"+name+"` CHARACTER SET utf8mb4 COLLATE utf8mb4_bin"); err != nil {
		t.Fatal("create isolated invocation database")
	}
	t.Cleanup(func() {
		// name is generated above, never an environment supplied database.
		cleanup := sql.OpenDB(controlConnector)
		defer cleanup.Close()
		cleanupCtx, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		if _, err := cleanup.ExecContext(cleanupCtx, "DROP DATABASE `"+name+"`"); err != nil {
			t.Error("drop isolated invocation database")
		}
	})
	cfg.DBName = name
	connector, err := drivermysql.NewConnector(cfg)
	if err != nil {
		t.Fatal("create isolated invocation connector")
	}
	pool := sql.OpenDB(connector)
	t.Cleanup(func() { _ = pool.Close() })
	db, err := gorm.Open(mysql.New(mysql.Config{Conn: pool}), &gorm.Config{Logger: logger.Discard})
	if err != nil {
		t.Fatal("open isolated invocation database")
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal("obtain isolated database pool")
	}
	sqlDB.SetMaxOpenConns(8)
	t.Cleanup(func() { _ = sqlDB.Close() })
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate tracked migrations")
	}
	migrations := []string{"0056_invocation_payloads.sql", "0057_invocation_payload_staging.sql", "0058_invocation_payload_accesses.sql", "0103_video_payload_capture_representation.sql"}
	if withPurgeIndex {
		migrations = append(migrations, "0132_video_payload_purge_indexes.sql")
	}
	for _, migration := range migrations {
		raw, err := os.ReadFile(filepath.Join(filepath.Dir(current), "..", "..", "..", "migrations", migration))
		if err != nil {
			t.Fatal(err)
		}
		if err := db.WithContext(ctx).Exec(string(raw)).Error; err != nil {
			t.Fatalf("apply tracked migration %s", migration)
		}
	}
	now := videoExecutionDatabaseNow(t, db)
	s, err := NewVideoStore(db, videoKeys(t, "old"), VideoStoreOptions{Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	return s, db, &now
}

func videoExecutionDatabaseNow(t *testing.T, db *gorm.DB) time.Time {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	var result struct{ DatabaseTime time.Time }
	if err := db.WithContext(ctx).Raw("SELECT UTC_TIMESTAMP(3) AS database_time").Scan(&result).Error; err != nil || result.DatabaseTime.IsZero() {
		t.Fatal("observe isolated primary database time")
	}
	return result.DatabaseTime.UTC().Truncate(time.Millisecond)
}

// Waiting observes the actual primary clock; it never edits expires_at/AAD or
// injects a production clock. The long bound includes the real 150s Blob grace.
func videoExecutionWaitUntil(t *testing.T, db *gorm.DB, at time.Time) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Minute)
	for time.Now().Before(deadline) {
		now := videoExecutionDatabaseNow(t, db)
		if !now.Before(at) {
			return
		}
		wait := 25 * time.Millisecond
		if at.Sub(now) > time.Second {
			wait = 250 * time.Millisecond
		}
		time.Sleep(wait)
	}
	t.Fatal("primary database time did not reach the fixture deadline")
}
