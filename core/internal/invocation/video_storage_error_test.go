package invocation

import (
	"errors"
	"strings"
	"testing"

	drivermysql "github.com/go-sql-driver/mysql"
)

func TestVideoStorageCauseIsClassifiableWithoutDiagnosticExposure(t *testing.T) {
	cause := &drivermysql.MySQLError{Number: 1213, Message: "private SQL payload or DSN"}
	err := videoStorageFailure(cause)
	var driverError *drivermysql.MySQLError
	if !errors.Is(err, ErrVideoStorage) || !errors.As(err, &driverError) || driverError.Number != 1213 || err.Error() != ErrVideoStorage.Error() || strings.Contains(err.Error(), "private") {
		t.Fatal("storage error lost typed cause or exposed diagnostics")
	}
}
