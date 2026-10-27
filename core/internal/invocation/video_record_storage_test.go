package invocation

import (
	"errors"
	"fmt"
	"testing"

	drivermysql "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

// This local transaction test injects typed driver causes at the real RecordTx
// insertion boundary. The full MySQL financial rollback test remains required.
func TestVideoRecordTxStorageCauseAndCallerRollback(t *testing.T) {
	for _, number := range []uint16{1054, 1213} {
		t.Run(fmt.Sprint(number), func(t *testing.T) {
			s, db, _ := videoSetup(t)
			cause := &drivermysql.MySQLError{Number: number, Message: "private SQL payload or DSN"}
			const callback = "test:record-storage-cause"
			hits := 0
			if err := db.Callback().Create().Before("gorm:create").Register(callback, func(q *gorm.DB) {
				if q.Statement.Table == "invocations" {
					hits++
					q.AddError(cause)
				}
			}); err != nil {
				t.Fatal("cannot register insertion failure")
			}
			t.Cleanup(func() { _ = db.Callback().Create().Remove(callback) })
			org := uint64(1)
			err := db.Transaction(func(tx *gorm.DB) error {
				if err := s.RequestStartTx(tx, VideoRequestStart{RequestID: "req-storage", OrgID: &org, AuthOutcome: "authenticated", Method: "POST", Route: "/v1/videos", Operation: "video_create"}); err != nil {
					return err
				}
				return RecordTx(tx, &Invocation{OrgID: org, APIKeyID: 4, RequestID: "req-storage", Model: "seedance", Modality: ModalityVideo, Status: StatusSuccess, StatusCode: 200, UsageSource: UsageSourceUpstream, CompletionTokens: 100})
			})
			var driver *drivermysql.MySQLError
			if hits != 1 || !errors.Is(err, ErrVideoStorage) || !errors.As(err, &driver) || driver != cause || errors.Unwrap(err) != cause {
				t.Fatal("RecordTx lost its storage sentinel or exact typed cause")
			}
			for _, format := range []string{"%s", "%v", "%+v"} {
				if fmt.Sprintf(format, err) != ErrVideoStorage.Error() {
					t.Fatal("storage error formatting exposed internal diagnostics")
				}
			}
			var requests, summaries int64
			if err := db.Model(&VideoRequestLog{}).Count(&requests).Error; err != nil {
				t.Fatal("cannot check caller transaction rollback")
			}
			if err := db.Model(&Invocation{}).Count(&summaries).Error; err != nil {
				t.Fatal("cannot check summary rollback")
			}
			if requests != 0 || summaries != 0 {
				t.Fatal("storage failure committed part of the caller transaction")
			}
		})
	}
}
