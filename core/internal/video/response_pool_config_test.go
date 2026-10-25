package video

import (
	"reflect"
	"strings"
	"testing"
	"time"

	drivermysql "github.com/go-sql-driver/mysql"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestResponsePoolConfigurationPreservesPrimaryAndMigrationSettings(t *testing.T) {
	dsn := "test-user:test-password@tcp(primary.invalid:3307)/test_evidence?parseTime=true&charset=utf8mb4&collation=utf8mb4_bin&timeout=37s&readTimeout=41s&writeTimeout=43s&tls=true&loc=UTC&innodb_lock_wait_timeout=50"
	before, err := drivermysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal("test configuration rejected")
	}
	dedicated, err := responsePoolConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if dedicated.Timeout != 2*time.Second || dedicated.ReadTimeout != 2*time.Second || dedicated.WriteTimeout != 2*time.Second {
		t.Fatal("dedicated socket bounds missing")
	}
	// Everything other than transport timing/logging remains the same primary
	// identity, including TLS and server session parameters.
	dedicated.Timeout, dedicated.ReadTimeout, dedicated.WriteTimeout = before.Timeout, before.ReadTimeout, before.WriteTimeout
	dedicated.Logger = before.Logger
	if !reflect.DeepEqual(dedicated, before) {
		t.Fatal("dedicated pool changed the primary connection identity/options")
	}
	after, err := drivermysql.ParseDSN(dsn)
	if err != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("runtime/migration DSN was mutated")
	}
	dedicated.Params["innodb_lock_wait_timeout"] = "1"
	if before.Params["innodb_lock_wait_timeout"] != "50" {
		t.Fatal("configuration clone shares mutable parameters")
	}
}

func TestResponsePoolConfigurationRejectsUnsafeInputWithoutDiagnostics(t *testing.T) {
	for _, dsn := range []string{"", "user:secret@tcp(host:3306)/", "user:secret@tcp(host:3306)/db", "secret-not-a-dsn"} {
		_, err := responsePoolConfig(dsn)
		if err != errResponsePoolConfiguration || strings.Contains(err.Error(), "secret") {
			t.Fatal("invalid configuration did not return a fixed safe error")
		}
	}
}

func TestResponsePoolConfigurationRejectsAnotherDatabaseBeforeConnecting(t *testing.T) {
	// GORM parses the primary identity without opening a socket. An attempted
	// secondary identity must be rejected before any new connector or ping.
	dsn := "user:secret@tcp(primary.invalid:3306)/original?parseTime=true"
	db, err := gorm.Open(gormmysql.New(gormmysql.Config{DSN: dsn, SkipInitializeWithVersion: true}), &gorm.Config{DisableAutomaticPing: true, Logger: logger.Discard})
	if err != nil {
		t.Fatal("could not construct no-network primary fixture")
	}
	pool, err := db.DB()
	if err != nil {
		t.Fatal("primary SQL pool unavailable")
	}
	defer pool.Close()
	s := &Service{DB: db}
	for _, other := range []string{
		"user:secret@tcp(other.invalid:3306)/original?parseTime=true",
		"user:secret@tcp(primary.invalid:3306)/different?parseTime=true",
		"user:other-secret@tcp(primary.invalid:3306)/original?parseTime=true",
	} {
		if err := s.ConfigureResponseInbox(other, true); err != errResponsePoolConfiguration || s.responseDB != nil || s.ResponseInboxEnabled {
			t.Fatal("a mismatched primary identity configured an evidence pool")
		}
	}
	if pool.Stats().OpenConnections != 0 {
		t.Fatal("rejected configuration accessed the primary connection")
	}
}
