// Package dberr classifies cross-dialect database errors (PostgreSQL SQLSTATE + SQLite tests).
package dberr

import (
	"errors"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
)

// IsDuplicate reports whether err represents a unique constraint violation (idempotency conflict).
func IsDuplicate(err error) bool {
	if err == nil {
		return false
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	s := err.Error()
	return strings.Contains(s, "UNIQUE constraint failed") ||
		strings.Contains(s, "constraint failed: UNIQUE") ||
		strings.Contains(s, "duplicate key value violates unique constraint") ||
		strings.Contains(s, "SQLSTATE 23505")
}
