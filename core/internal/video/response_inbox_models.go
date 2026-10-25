package video

import "time"

const responseInboxProtocol = "inbox-v1"

// ExecutionAttempt fixes the identity of a claim before any provider call.
// Its presence selects inbox recovery even when its protocol is unsupported.
// Registration never grants permission to send or apply a response by itself.
type ExecutionAttempt struct {
	AttemptID                string `gorm:"primaryKey;column:attempt_id"`
	OrgID                    uint64
	TaskID                   string
	OperationID              string
	TargetUpstreamTaskID     *string `gorm:"column:target_upstream_task_id" json:",omitempty"`
	Action                   string
	StorageProtocol          string
	ProviderScopeID          uint64
	CredentialSnapshotID     uint64
	BillingSnapshotID        uint64
	InputPayloadRef          string
	InputIdentityHMAC        string `gorm:"column:input_identity_hmac"`
	InputHMACVersion         string `gorm:"column:input_hmac_version"`
	RuntimeVersion           string
	AdapterVersion           string
	ProtocolContractVersion  string
	BillingSchemaVersion     string
	ExecutionSchemaVersion   uint32
	ObservationSchemaVersion uint32
	OperationClaimVersion    uint64
	EvidenceHMACVersion      string `gorm:"column:evidence_hmac_version"`
	CaptureHistory           bool
	RetentionDays            int
	RegisteredAt             time.Time
}

func (ExecutionAttempt) TableName() string { return "video_execution_attempts" }

// ResponseInbox is immutable evidence, not an operation projection. Body,
// URLs and credentials live exclusively in Invocation Store.
type ResponseInbox struct {
	ReceiptID                string `gorm:"primaryKey;column:receipt_id"`
	OrgID                    uint64
	TaskID                   string
	OperationID              string
	AttemptID                string
	Action                   string
	StorageProtocol          string
	ProviderScopeID          uint64
	CredentialSnapshotID     uint64
	BillingSnapshotID        uint64
	RuntimeVersion           string
	AdapterVersion           string
	ProtocolContractVersion  string
	BillingSchemaVersion     string
	ExecutionSchemaVersion   uint32
	ObservationSchemaVersion uint32
	EvidenceHMACVersion      string `gorm:"column:evidence_hmac_version"`
	EvidenceHMAC             string `gorm:"column:evidence_hmac"`
	HTTPStatus               int    `gorm:"column:http_status"`
	Outcome                  string
	SafeErrorCode            string
	UpstreamTaskID           string
	ProviderRequestID        string
	Truncated                bool
	CapturedBodyBytes        int64
	PayloadRef               string
	ReceivedAt               time.Time
}

func (ResponseInbox) TableName() string { return "video_response_inbox" }

// ReceiptApplication owns only replay progress. Receipt ownership is immutable;
// monetary facts remain in videobilling and Ledger.
type ReceiptApplication struct {
	ReceiptID      string `gorm:"primaryKey;column:receipt_id"`
	OrgID          uint64
	TaskID         string
	AttemptID      string
	State          string
	Version        uint64
	NextAttemptAt  time.Time
	ErrorCode      string
	AppliedAt      *time.Time
	AppliedEventID string
	BillingEntryID *uint64
	CreatedAt      time.Time
	UpdatedAt      time.Time
}

func (ReceiptApplication) TableName() string { return "video_receipt_applications" }
