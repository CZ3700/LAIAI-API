package video

import "fmt"

// Request IDs are globally unique, unlike tenant-scoped idempotency keys.
// The real-MySQL fixtures deliberately preserve prior runs' rows.
func (f *fixture) requestID(label string) string {
	return fmt.Sprintf("fixture_%d_%s", f.key.OrgID, label)
}
