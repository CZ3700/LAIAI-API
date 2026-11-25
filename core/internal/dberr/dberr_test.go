package dberr

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestIsDuplicate(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"postgres 23505 typed", &pgconn.PgError{Code: "23505", Message: "duplicate key value violates unique constraint"}, true},
		{"postgres other code", &pgconn.PgError{Code: "42601", Message: "syntax error"}, false},
		{"postgres 23505 wrapped", errfmt(&pgconn.PgError{Code: "23505"}), true},
		{"sqlite unique text", errors.New("UNIQUE constraint failed: users.email_canonical"), true},
		{"sqlite constraint alt", errors.New("constraint failed: UNIQUE (org_id, user_id)"), true},
		{"unrelated", errors.New("connection refused"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := IsDuplicate(c.err); got != c.want {
				t.Errorf("IsDuplicate(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}

func errfmt(err error) error { return wrapped{err} }

type wrapped struct{ inner error }

func (w wrapped) Error() string { return "gorm: " + w.inner.Error() }
func (w wrapped) Unwrap() error { return w.inner }
