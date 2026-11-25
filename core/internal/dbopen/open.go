// Package dbopen provides unified PostgreSQL and SQLite GORM database opening.
package dbopen

import (
	"fmt"
	"strings"

	"github.com/glebarez/sqlite"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// IsSQLiteDSN reports whether dsn targets SQLite (unit/local tests) rather than PostgreSQL.
func IsSQLiteDSN(dsn string) bool {
	trimmed := strings.TrimSpace(dsn)
	return trimmed == ":memory:" ||
		strings.HasPrefix(trimmed, "file:") ||
		strings.HasSuffix(trimmed, ".db") ||
		strings.HasSuffix(trimmed, ".sqlite")
}

// Open opens a PostgreSQL (pgx/v5) or SQLite GORM handle matching the supplied DSN.
func Open(dsn string, cfg *gorm.Config) (*gorm.DB, error) {
	trimmed := strings.TrimSpace(dsn)
	if trimmed == "" {
		return nil, fmt.Errorf("dbopen: empty DSN")
	}
	if cfg == nil {
		cfg = &gorm.Config{}
	}
	if IsSQLiteDSN(trimmed) {
		return gorm.Open(sqlite.Open(trimmed), cfg)
	}
	return gorm.Open(postgres.Open(trimmed), cfg)
}
