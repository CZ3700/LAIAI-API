# Ark supplier bill administration

Mount `registerArkBillingAdmin(su)` beneath the existing `/api/admin` superadmin group. These endpoints additionally require an active individual superadmin **Bearer session**. A shared `X-Admin-Token` is not an identifiable reviewer and is rejected, including when it takes precedence over an accompanying session. Proposer/reviewer IDs are never accepted in JSON.

The source acquisition mode in this implementation is **operator_upload**: an operator exports the official supplier bill, normalizes its documented fields, and uploads the structured document; a different authenticated operator reviews the official bill ID, source revision order, task-total meaning, model mapping, and numeric fields. This is recorded human attestation. It is not automatic cryptographic verification that the supplier produced the file. No endpoint is described as an official supplier billing API fetch. Until independent source review succeeds, importing/settling that source fails closed.

Every command accepts one strict JSON object (unknown/duplicate fields, trailing JSON, float Tokens, overflow integers, and bodies above 1 MiB are rejected). Monetary amounts are decimal strings with at most 12 integral and 8 fractional digits. Each source supports up to 1000 rows. Split larger exports into explicitly identified source documents while preserving stable supplier row IDs and verified revisions. Do not fabricate revision order or proportionally allocate aggregate bills. A `task_total` row must describe one complete authoritative task charge in CNY; aggregate rows remain quarantined and never settle tasks.

## API

All paths below are relative to `/api/admin/video`:

| Method/path | JSON body / purpose |
|---|---|
| `POST /bill-evidence` | `{source_key, document}`; encrypt source and record uploader |
| `GET /bill-evidence/:source_id` | Audited decryption for source review |
| `POST /bill-evidence/:source_id/review` | `{approved:true,reason:"verified_official_export"}` or `{approved:false,reason:"source_not_verified"}`; different user required |
| `POST /bill-imports` | `{source_id,import_key}`; derive hash/count/version from verified evidence |
| `POST /bill-imports/:import_id/lines` | `{row_number}`; derive all facts from the reviewed row |
| `POST /bill-imports/:import_id/complete` | `{}`; requires all declared ordinals |
| `POST /bill-lines/:line_id/bind` | `{}`; resolve the persisted unique provider/upstream binding |
| `POST /orgs/:org_id/tasks/:task_id/settle-bill` | `{line_id}`; explicit initial usage recovery only after trustworthy `succeeded` task state |
| `POST /orgs/:org_id/tasks/:task_id/adjustments` | `{proposal_key,line_id,reason:"verified_invoice_correction",supplier_only:true,expires_at}`; `supplier_only` required explicitly; expiry <= 7 days |
| `GET /orgs/:org_id/adjustments/:proposal_id` | Exact proposed customer delta and target supplier/Token facts with reviewed source provenance |
| `POST /orgs/:org_id/adjustments/:proposal_id/review` | `{approved:true,reason:"verified_bill"}` or `{approved:false,reason:"correction_not_approved"}`; different user required |
| `POST /orgs/:org_id/adjustments/:proposal_id/apply` | `{}`; apply approved, current, unexpired immutable proposal |

Document schema (these numerical fields appear only on the source upload endpoint):

```json
{
  "schema": "ark_supplier_bill_v1",
  "provider_scope_id": 11,
  "official_bill_id": "official-export-202610",
  "source_version": "export-v1",
  "currency": "CNY",
  "rows": [{
    "source_record_id": "official-row-1",
    "source_version": "row-v1",
    "source_revision": 1,
    "granularity": "task_total",
    "upstream_task_id": "upstream-task-id",
    "model": "seedance-2.0",
    "period_start": "2026-10-01T00:00:00Z",
    "period_end": "2026-11-01T00:00:00Z",
    "completion_tokens": 54000,
    "amount_cny": "2.48400000",
    "observed_at": "2026-11-02T00:00:00Z"
  }]
}
```

`completion_tokens:null` preserves unknown usage and the existing hold. The SHA-256 describes the canonical structured document stored encrypted; it is not claimed as a digest of an absent original PDF/CSV. The official bill ID links the normalized document to the operator-reviewed supplier export. No public customer-provided amount, Token count, binding, role, actor, scope override, or payload pointer can alter a financial command.

Source ciphertext is retained for 30 days in Invocation Store under the uploading administrator's session Org. The source registry explicitly records that storage Org and capture request ID. An independently reviewed provider scope can contain tasks from multiple customer Orgs; task attribution is derived from `(provider_scope_id,upstream_task_id)` and verified against Task/State/model in the same transaction, never from uploaded Org/task columns. `binding_version` is a server-generated SHA-256 of a versioned structured binding identity.

Reads are independently audited and verify encryption metadata, source SHA, source schema, and expiry. Mutations put immutable financial entries, task projections, events/outbox and mandatory audit rows in the same transaction. Any audit/outbox/projection error rolls back money. Source review and adjustment review are distinct controls; an approved source does not authorize a financial correction. A timeout, unknown submission, aggregate row, missing usage, or ambiguous deletion never authorizes release. Source expiry prevents new financial actions until fresh, independently reviewed source evidence is available. Replaying an already committed adjustment returns its original immutable receipt, even after expiry or later corrections; it does not move money or create another task event.

To refresh expired evidence, acquire the same official row again, upload it with a new `source_key`, obtain independent source review, and import with a new `import_key`. Preserve the supplier's original row identity and revision. Import deduplication retains the original immutable BillLine; its additional import receipt links the newly reviewed source. Financial commands resolve that receipt and compare every canonical row field with the immutable line. Their task event/audit references the current verified source, while historical line/evidence references remain unchanged. Do not invent a higher supplier revision to renew retention.

Verification includes strict-body and shared-token unit tests plus five real-MySQL route tests covering source/financial separation, hard integer precision, duplicate financial commands/events, source refresh, unknown usage freezing, aggregate quarantine, incomplete imports, evidence tampering, and forced audit/outbox failures rolling back money. Real database tests require the separately migrated `ARK_TEST_MYSQL_DSN` pointing to `ark_test`; they never run ORM migrations, reset shared tables, or call paid Ark endpoints.

## Root-owned migration DDL

One new table; assign the next migration number in the integration branch. The provenance row has one immutable source and at most one immutable decision. Updates from `pending_review` are performed while holding the row lock. Replays must match the original actor/decision exactly.

```sql
CREATE TABLE video_bill_evidence_sources (
 id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
 source_key VARCHAR(64) COLLATE utf8mb4_bin NOT NULL,
 provider_scope_id BIGINT UNSIGNED NOT NULL,
 storage_org_id BIGINT UNSIGNED NOT NULL,
 source_kind VARCHAR(32) COLLATE utf8mb4_bin NOT NULL,
 official_bill_id VARCHAR(128) COLLATE utf8mb4_bin NOT NULL,
 source_version VARCHAR(64) COLLATE utf8mb4_bin NOT NULL,
 source_file_sha256 CHAR(64) COLLATE utf8mb4_bin NOT NULL,
 payload_ref VARCHAR(128) COLLATE utf8mb4_bin NOT NULL,
 capture_request_id VARCHAR(128) COLLATE utf8mb4_bin NOT NULL,
 row_count BIGINT NOT NULL,
 uploaded_by BIGINT UNSIGNED NOT NULL,
 state VARCHAR(32) COLLATE utf8mb4_bin NOT NULL,
 reviewed_by BIGINT UNSIGNED NOT NULL DEFAULT 0,
 review_reason VARCHAR(64) COLLATE utf8mb4_bin NOT NULL DEFAULT '',
 reviewed_at DATETIME(3) NULL,
 created_at DATETIME(3) NOT NULL,
 expires_at DATETIME(3) NOT NULL,
 UNIQUE KEY uk_video_bill_source_key(provider_scope_id,source_key),
 UNIQUE KEY uk_video_bill_source_payload(payload_ref),
 KEY idx_video_bill_source_review(state,id),
 KEY idx_video_bill_source_org(storage_org_id,id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
```
