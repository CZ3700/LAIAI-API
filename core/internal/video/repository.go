package video

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/laiai/laiai-next/core/internal/task"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrInvalid         = errors.New("video: invalid request")
	ErrUnavailable     = errors.New("video: unavailable")
	ErrNotFound        = errors.New("video: not found")
	ErrConflict        = errors.New("video: conflict")
	ErrState           = errors.New("video: invalid state")
	ErrCapability      = errors.New("video: unsupported capability")
	ErrForbidden       = errors.New("video: forbidden")
	ErrExecutionPolicy = errors.New("video: organization execution not enabled")
)

func randomID(prefix string) (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(b[:]), nil
}

func locked(tx *gorm.DB) *gorm.DB {
	if tx.Dialector.Name() == "mysql" {
		return tx.Clauses(clause.Locking{Strength: "UPDATE"})
	}
	return tx
}

func digest(secret []byte, domain string, parts ...string) string {
	raw, _ := json.Marshal(append([]string{domain}, parts...))
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(raw)
	return hex.EncodeToString(mac.Sum(nil))
}

func validIdempotency(key string) bool {
	if len(key) < 8 || len(key) > 128 {
		return false
	}
	for _, c := range key {
		if c < 33 || c > 126 {
			return false
		}
	}
	return true
}

// lookupIdempotency does not reserve a new key. Admission limits apply only
// after this authenticated, tenant-scoped replay lookup has found no request.
func lookupIdempotency(db *gorm.DB, secret []byte, org uint64, method, path, key string, body []byte) (IdempotencyRecord, bool, error) {
	if len(secret) < 32 || org == 0 || !validIdempotency(key) {
		return IdempotencyRecord{}, false, ErrInvalid
	}
	var version HashVersion
	err := db.Where("version = ?", "v1").First(&version).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return IdempotencyRecord{}, false, nil
	}
	if err != nil {
		return IdempotencyRecord{}, false, err
	}
	if !hmac.Equal([]byte(version.Fingerprint), []byte(digest(secret, "version-fingerprint:v1"))) {
		return IdempotencyRecord{}, false, ErrUnavailable
	}
	scope := fmt.Sprint(org)
	var row IdempotencyRecord
	err = db.Where("org_id = ? AND method = ? AND operation = ? AND key_hmac = ?", org, method, path, digest(secret, "idempotency-key:v1", scope, method, path, key)).First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return row, false, nil
	}
	if err != nil {
		return row, false, err
	}
	if !hmac.Equal([]byte(row.BodyHMAC), []byte(digest(secret, "idempotency-body:v1", scope, method, path, string(body)))) {
		return row, false, ErrConflict
	}
	return row, true, nil
}

func claimIdempotencyTx(tx *gorm.DB, secret []byte, org uint64, method, path, key string, body []byte, resource, operation string, now time.Time) (IdempotencyRecord, bool, error) {
	if len(secret) < 32 || org == 0 || !validIdempotency(key) {
		return IdempotencyRecord{}, false, ErrInvalid
	}
	version := HashVersion{Version: "v1", Fingerprint: digest(secret, "version-fingerprint:v1")}
	if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&version).Error; err != nil {
		return IdempotencyRecord{}, false, err
	}
	var existingVersion HashVersion
	if err := locked(tx).First(&existingVersion, "version = ?", "v1").Error; err != nil {
		return IdempotencyRecord{}, false, err
	}
	if !hmac.Equal([]byte(existingVersion.Fingerprint), []byte(version.Fingerprint)) {
		return IdempotencyRecord{}, false, ErrUnavailable
	}
	scope := fmt.Sprint(org)
	row := IdempotencyRecord{OrgID: org, Method: method, Operation: path, KeyHMAC: digest(secret, "idempotency-key:v1", scope, method, path, key), BodyHMAC: digest(secret, "idempotency-body:v1", scope, method, path, string(body)), HashVersion: "v1", ResourceID: resource, OperationID: operation, CreatedAt: now}
	result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
	if result.Error != nil {
		return row, false, result.Error
	}
	if result.RowsAffected == 1 {
		return row, false, nil
	}
	var previous IdempotencyRecord
	if err := locked(tx).Where("org_id = ? AND method = ? AND operation = ? AND key_hmac = ?", org, method, path, row.KeyHMAC).First(&previous).Error; err != nil {
		return row, false, err
	}
	if !hmac.Equal([]byte(previous.BodyHMAC), []byte(row.BodyHMAC)) {
		return row, false, ErrConflict
	}
	return previous, true, nil
}

func loadTask(db *gorm.DB, org uint64, id string, lock bool) (task.Task, State, error) {
	var t task.Task
	var st State
	if org == 0 || !strings.HasPrefix(id, "task_") {
		return t, st, ErrNotFound
	}
	q := db
	if lock {
		q = locked(db)
	}
	if err := q.Where("org_id = ? AND task_id = ? AND provider = ?", org, id, Provider).First(&t).Error; err != nil {
		return t, st, err
	}
	q = db
	if lock {
		q = locked(db)
	}
	if err := q.Where("org_id = ? AND task_id = ? AND deleted_at IS NULL", org, id).First(&st).Error; err != nil {
		return t, st, err
	}
	return t, st, nil
}

func appendEventTx(tx *gorm.DB, t *task.Task, st *State, eventType, from, to, requestID, evidence string, now time.Time) error {
	eventID, err := randomID("evt_")
	if err != nil {
		return err
	}
	st.EventSequence++
	ev := Event{ID: eventID, OrgID: t.OrgID, TaskID: t.TaskID, Sequence: st.EventSequence, Type: eventType, RequestID: requestID, FromStatus: from, ToStatus: to, BillingState: st.BillingState, EvidenceRef: evidence, CreatedAt: now}
	if err := tx.Create(&ev).Error; err != nil {
		return err
	}
	if err := tx.Model(&State{}).Where("org_id = ? AND task_id = ?", t.OrgID, t.TaskID).Updates(map[string]any{"event_sequence": st.EventSequence, "updated_at": now}).Error; err != nil {
		return err
	}
	return tx.Create(&Outbox{EventID: eventID, OrgID: t.OrgID, TaskID: t.TaskID, Topic: eventType, Status: "pending", NextAttemptAt: now, CreatedAt: now}).Error
}

func taskView(t task.Task, st State) TaskView {
	v := TaskView{ID: t.TaskID, Model: t.Model, Status: t.Status, BillingState: st.BillingState, SyncState: st.SyncState, ArtifactStatus: st.ArtifactStatus, CreatedAt: st.CreatedAt, UpdatedAt: st.UpdatedAt, LastSyncedAt: st.LastSyncedAt, DeletionState: "none"}
	if st.ObservedCompletionTokens != nil || st.TotalTokens != nil {
		v.Usage = &UsageView{CompletionTokens: st.ObservedCompletionTokens, TotalTokens: st.TotalTokens}
	}
	if st.DeletionOperationID != "" {
		v.DeletionState = "pending"
	}
	return v
}
