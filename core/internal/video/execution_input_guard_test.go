package video

import (
	"context"
	"errors"
	"testing"

	"github.com/laiai/laiai-next/core/internal/invocation"
	"github.com/laiai/laiai-next/core/internal/videoruntime"
)

func TestExecutionInputNotSentErrorPrecedence(t *testing.T) {
	for _, err := range []error{invocation.ErrVideoExpired, invocation.ErrVideoRetentionDenied, invocation.ErrVideoStorage, invocation.ErrVideoAuditUnavailable} {
		if !executionReadAllowsNotSent(err) {
			t.Fatal("known lifecycle/storage failure lost its identity-guarded no-send path", err)
		}
	}
	for _, err := range []error{context.Canceled, context.DeadlineExceeded, invocation.ErrVideoExecutionAuthorityUnavailable, invocation.ErrVideoSchemaUnsupported, invocation.ErrVideoAccessDenied, invocation.ErrVideoCorrupt, invocation.ErrVideoConflict, invocation.ErrVideoNotFound, invocation.ErrVideoInvalid, videoruntime.ErrUnsupported, ErrConflict} {
		if executionReadAllowsNotSent(errors.Join(invocation.ErrVideoExpired, invocation.ErrVideoStorage, invocation.ErrVideoAuditUnavailable, err)) {
			t.Fatal("lifecycle/storage/audit error concealed a higher-priority denial", err)
		}
	}
	if executionReadAllowsNotSent(errors.New("unclassified input error")) {
		t.Fatal("unclassified error was treated as definite no-send")
	}
}
