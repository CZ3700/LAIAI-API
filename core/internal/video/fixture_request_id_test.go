package video

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"

	"github.com/laiai/laiai-next/core/internal/identity"
)

// Request IDs are globally unique, unlike tenant-scoped idempotency keys.
// The real-MySQL fixtures deliberately preserve prior runs' rows.
func (f *fixture) requestID(label string) string {
	digest := sha256.Sum256([]byte(fmt.Sprintf("fixture/%d/%s", f.key.OrgID, label)))
	return fmt.Sprintf("%x", digest[:16])
}

func TestFixtureRequestIDFitsPersistentSchema(t *testing.T) {
	f := &fixture{key: &identity.APIKey{OrgID: ^uint64(0)}}
	label := strings.Repeat("long-request-label", 10)
	id := f.requestID(label)
	if len(id) != 32 || id != f.requestID(label) {
		t.Fatal("fixture request ID must be stable and exactly 32 characters")
	}
	if id == f.requestID(label+"-next") {
		t.Fatal("request labels collided")
	}
	f.key.OrgID--
	if id == f.requestID(label) {
		t.Fatal("requests from independent fixture tenants collided")
	}
}
