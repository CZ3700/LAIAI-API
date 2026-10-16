# Durable video evidence

This opt-in package path leaves the legacy asynchronous Recorder unchanged. It
does not add HTTP endpoints, business outboxes, pricing, task transitions, an S3
client, or organization policy resolution. Those belong to the orchestrator.

## Integration contract

```go
NewVideoStore(db *gorm.DB, keyring *crypto.Keyring, options VideoStoreOptions) (*VideoStore, error)
(*VideoStore).Prepare(VideoPayloadInput) (*PreparedVideoPayload, error)
(*PreparedVideoPayload).PayloadID() string
(*VideoStore).StageTx(tx *gorm.DB, prepared *PreparedVideoPayload) (string, error)
(*VideoStore).ReadForExecution(ctx context.Context, orgID uint64, payloadID string, access VideoAccess) ([]byte, error)
(*VideoStore).ReadForRecovery(ctx context.Context, orgID uint64, payloadID string, access VideoAccess) ([]byte, error)
(*VideoStore).ReadForHistory(ctx context.Context, orgID uint64, payloadID string, access VideoAccess) ([]byte, error)
(*VideoStore).RequestStartTx(tx *gorm.DB, input VideoRequestStart) error
(*VideoStore).RequestFinishTx(tx *gorm.DB, input VideoRequestFinish) error
RecordTx(tx *gorm.DB, input *Invocation) error
(*VideoStore).UploadPending(ctx context.Context, workerID string, batch int) (int64, error)
(*VideoStore).PurgeExpired(ctx context.Context, batch int) (int64, error)
```

Call Prepare before opening a transaction; StageTx uses that transaction and a
savepoint to atomically write metadata and ciphertext staging. No network I/O
occurs inside StageTx. Reuse the same prepared value for local transaction
retries. A fresh Prepare with an occupied correlation fails with Conflict, even
if its plaintext happened to be equivalent. Resolve business idempotency before
preparation; never deduplicate across tenants or by plaintext hashes.

CaptureEnabled is explicit. Disabled historical capture creates metadata only.
Execution requires explicit retention consent and effective_request direction;
strict no-persistence organizations must be rejected by the async API. Retention
defaults to 30 days and cannot exceed 30 days; the orchestrator chooses a shorter
execution TTL that covers its allowed recovery window. Historical capture is
not a substitute for an execution capsule. To recover upstream receipts, retain
history/upstream_response with its own policy; recovery is unavailable when it
is disabled or expired. HTTP status, upstream request ID, classification and
financial facts must remain in separate durable business metadata.

Random 256-bit DEKs encrypt each body using AES-GCM. The configured keyring wraps
the DEK; old key IDs must remain available for retained payloads during rotation.
AAD binds org, payload/request/attempt identity, direction, purpose, schema,
redaction version, body format and expiry. SQL staging and the optional private
blob store contain ciphertext only; general logs contain safe metadata. A
ciphertext hash detects damaged objects but is never a plaintext fingerprint.

Input is body-only; never pass headers or credentials as metadata. The boundary
removes named secret JSON fields, known SecretValues, Bearer credentials,
recognized inline assignments and URL credentials/query secrets. Signed media
URLs may remain solely inside encrypted bodies. This is not a general detector
of arbitrary secrets in prose: the caller must supply its credential values and
only allow typed provider/body fields. Execution inputs requiring redaction are
rejected so the submitted task cannot silently change. Historical JSON is
canonicalized after redaction. Non-JSON upstream responses use BodyFormat
redacted_text_v1 and a JSON {format,text,original_bytes,truncated} envelope after
UTF-8 replacement and redaction. If text escaping exceeds the envelope limit,
text is shortened with an explicit truncated flag. Other non-JSON directions
are invalid. The default body limit is 10 MiB and can be configured up to
64 MiB; oversized source bodies are rejected and JSON is never truncated.

VideoAccess must be built from authenticated server context, never client DTOs.
All reads require explicit org scope and persist started and finished access
events. An audit failure prevents returning plaintext. Execution reads require
execution_worker + execution/effective_request. Recovery requires
execution_worker + history/upstream_response. History requires org_reader or
root + history; neither role grants execution reads. Root still supplies an
authorized org. These methods are called outside a business transaction; the
audit writes use their own commits. Caller must enforce per-operation RBAC,
bind worker jobs to an org and payload ID, and clear returned bytes after use.

RequestStartTx must commit before paid dispatch. Route is a trusted canonical
router template, never the incoming URL; identity numbers are nullable before
authentication. RequestFinishTx includes the same OrgID, uses a status CAS and
cannot overwrite completed records. Same-input repeats are accepted; different
input is Conflict. Crashed HTTP requests remain started until a reconciler
records incomplete with HTTPStatus 0. Request logs and task events are separate.
The orchestrator writes request payload pointers only for the owning org.

RecordTx is synchronous, participates in the caller's transaction and inserts an
immutable final video invocation summary. Only known upstream usage is accepted
and body-derived error text/client headers are rejected. Legacy request_id is
VARCHAR(32), so this function requires IDs no longer than 32 bytes; other new
tables allow 64. Use the original gateway request ID rather than a task ID. The
video ledger retains missing/unknown usage separately; do not call RecordTx with
fabricated zero tokens.

UploadPending claims staging via leases and fencing tokens, uploads only
ciphertext to a deterministic private object key, then commits stored state and
staging removal together. Failed or interrupted uploads keep staging and reuse
the same key/bytes. BlobStore Put/Delete must be idempotent and all operations
must honor context cancellation; the package bounds blob writes to 30 seconds.
TTL checks deny reads immediately. PurgeExpired removes wrapped DEKs/nonces and
staging in a transaction; ciphertext objects are deleted after an upload grace
period, with tombstones retained for retries. Schedule both workers explicitly.
SQL-only staging remains readable and purgeable if no blob backend is supplied.
Metadata, access audits and financial retention are independent of payload TTL.

## Formal migration requirements

The orchestrator owns production migration numbering. Do not AutoMigrate.
MySQL tables use InnoDB, utf8mb4, DATETIME(3) UTC timestamps and ASCII binary
collation on opaque IDs/correlation keys so request IDs are case-sensitive.
Empty strings are explicit defaults, not NULL, except fields marked nullable.
No foreign key may cascade task deletion into evidence or accounting records.

```sql
CREATE TABLE invocation_payloads (
 payload_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
 org_id BIGINT UNSIGNED NOT NULL,
 request_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 attempt_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '',
 correlation_key CHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 direction VARCHAR(32) NOT NULL, purpose VARCHAR(16) NOT NULL,
 schema_version INT UNSIGNED NOT NULL, body_format VARCHAR(32) NOT NULL,
 encryption_key_id VARCHAR(128) NOT NULL DEFAULT '', wrapped_key TEXT NOT NULL,
 nonce VARBINARY(32) NOT NULL, ciphertext_sha256 VARCHAR(64) NOT NULL DEFAULT '',
 plaintext_bytes BIGINT NOT NULL DEFAULT 0, redaction_version VARCHAR(32) NOT NULL,
 capture_status VARCHAR(16) NOT NULL, object_ref VARCHAR(256) NOT NULL DEFAULT '',
 created_at DATETIME(3) NOT NULL, expires_at DATETIME(3) NOT NULL,
 cleanup_after DATETIME(3) NULL, object_deleted_at DATETIME(3) NULL,
 UNIQUE KEY uk_video_payload_correlation(correlation_key),
 KEY idx_video_payload_org_request(org_id,request_id),
 KEY idx_video_payload_expiry(capture_status,expires_at),
 KEY idx_video_payload_cleanup(capture_status,object_deleted_at,cleanup_after)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE invocation_payload_staging (
 payload_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
 ciphertext LONGBLOB NOT NULL, attempts INT NOT NULL DEFAULT 0,
 next_attempt_at DATETIME(3) NOT NULL, lease_owner VARCHAR(64) NOT NULL DEFAULT '',
 lease_expires_at DATETIME(3) NULL, fencing_token BIGINT UNSIGNED NOT NULL DEFAULT 0,
 created_at DATETIME(3) NOT NULL,
 KEY idx_video_payload_upload(next_attempt_at,lease_expires_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE invocation_payload_accesses (
 access_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 phase VARCHAR(16) NOT NULL, org_id BIGINT UNSIGNED NOT NULL,
 payload_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 actor_role VARCHAR(32) NOT NULL, actor_id VARCHAR(64) NOT NULL,
 reason_code VARCHAR(64) NOT NULL, operation_id VARCHAR(64) NOT NULL DEFAULT '',
 outcome VARCHAR(16) NOT NULL, safe_error_code VARCHAR(64) NOT NULL DEFAULT '',
 created_at DATETIME(3) NOT NULL,
 PRIMARY KEY(access_id,phase),
 KEY idx_video_payload_access_org(org_id,payload_id,created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE api_request_logs (
 request_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
 trace_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '',
 org_id BIGINT UNSIGNED NULL, user_id BIGINT UNSIGNED NULL, api_key_id BIGINT UNSIGNED NULL,
 auth_outcome VARCHAR(16) NOT NULL, method VARCHAR(8) NOT NULL,
 route VARCHAR(160) NOT NULL, operation VARCHAR(64) NOT NULL,
 resource_type VARCHAR(64) NOT NULL DEFAULT '', resource_id VARCHAR(64) NOT NULL DEFAULT '',
 request_bytes BIGINT NOT NULL DEFAULT 0, request_payload_id VARCHAR(64) NOT NULL DEFAULT '',
 started_at DATETIME(3) NOT NULL,
 http_status INT NOT NULL DEFAULT 0, response_bytes BIGINT NOT NULL DEFAULT 0,
 response_payload_id VARCHAR(64) NOT NULL DEFAULT '', safe_error_code VARCHAR(64) NOT NULL DEFAULT '',
 outcome VARCHAR(16) NOT NULL, finished_at DATETIME(3) NULL,
 KEY idx_video_request_org(org_id,started_at),
 KEY idx_video_request_unfinished(outcome,started_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
```

Migration acceptance must verify the four tables against these Go fields and
exercise MySQL concurrency. SQLite unit tests are not evidence of MySQL lock or
deployment behavior. Current code adds no real paid provider calls.
