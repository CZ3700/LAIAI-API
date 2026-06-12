package dberr

import (
	"errors"
	"testing"

	"github.com/go-sql-driver/mysql"
)

func TestIsDuplicate(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"mysql 1062 typed", &mysql.MySQLError{Number: 1062, Message: "Duplicate entry"}, true},
		{"mysql other code", &mysql.MySQLError{Number: 1064, Message: "syntax"}, false},
		{"mysql 1062 wrapped", errfmt(&mysql.MySQLError{Number: 1062}), true},
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

// errfmt 模拟 GORM 包装后的错误（errors.As 仍能解包到 *mysql.MySQLError）
func errfmt(err error) error { return wrapped{err} }

type wrapped struct{ inner error }

func (w wrapped) Error() string { return "gorm: " + w.inner.Error() }
func (w wrapped) Unwrap() error { return w.inner }
