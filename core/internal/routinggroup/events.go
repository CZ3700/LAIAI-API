package routinggroup

import (
	"regexp"
	"time"

	"gorm.io/gorm"
)

// Audit identifies a server-authenticated actor, never a client-selected actor.
// Actor 0 is reserved for internal key lifecycle calls, not group management.
type Audit struct {
	ActorUserID uint64
	RequestID   string
}

var requestIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

func (a Audit) valid() bool { return a.RequestID == "" || requestIDPattern.MatchString(a.RequestID) }

// RecordKeyEvent is the narrow identity -> routinggroup boundary. The caller
// must use the SAME transaction as the key mutation. This function never reads
// identity's private tables, emits credentials, or opens another transaction.
func RecordKeyEvent(tx *gorm.DB, keyID, orgID, version uint64, kind string, a Audit) error {
	if tx == nil || keyID == 0 || orgID == 0 || version == 0 || !a.valid() || (kind != "key_issued" && kind != "key_revoked") {
		return ErrInvalid
	}
	if _, ok := tx.Statement.ConnPool.(gorm.TxCommitter); !ok {
		return gorm.ErrInvalidTransaction
	}
	return tx.Create(&Event{AggregateType: "key", AggregateID: keyID, AggregateVersion: version, EventKind: kind, OrgID: &orgID, KeyID: &keyID, ActorUserID: a.ActorUserID, RequestID: a.RequestID, CreatedAt: time.Now().UTC()}).Error
}

func event(tx *gorm.DB, aggregate string, id, version uint64, kind string, org, group, channel uint64, a Audit) error {
	e := Event{AggregateType: aggregate, AggregateID: id, AggregateVersion: version, EventKind: kind, ActorUserID: a.ActorUserID, RequestID: a.RequestID, CreatedAt: time.Now().UTC()}
	if org != 0 {
		e.OrgID = &org
	}
	if group != 0 {
		e.GroupID = &group
	}
	if channel != 0 {
		e.ChannelID = &channel
	}
	return tx.Create(&e).Error
}
