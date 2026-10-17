# Video billing: transactional pricing and reconciliation

Implemented here: exact decimal quote from verified metering bounds, immutable task price snapshots, hard Key quota plus multi-dimensional budget reservations, wallet hold/capture/release, nullable real usage evidence, one initial settlement, resumable supplier bill import, and independently reviewed append-only corrections. This package performs no HTTP, task lifecycle changes, migrations, or event/outbox writes. Those responsibilities belong to orchestration.

## Integration

```go
Quote(rule PricingRule, in MeteringInput) (QuoteResult, error)
QuoteWithMinimumTx(tx *gorm.DB, rule PricingRule, in MeteringInput, ctx MinimumContext) (QuoteResult, error)
ResolvePricingTx(tx *gorm.DB, key PricingKey, at time.Time) (PricingRule, error)
ReserveTx(tx *gorm.DB, in ReserveInput) (ReservationResult, error)
SettleUsageTx(tx *gorm.DB, in SettlementInput) (SettlementResult, error)
ReleaseConfirmedTx(tx *gorm.DB, in ReleaseInput) (SettlementResult, error)
```

Every Tx function requires a real GORM transaction and never commits it. Every returned error requires the caller to roll back. The caller commits task CAS, billing, encrypted evidence staging, events and outbox together. A successful function return is not permission to ignore a subsequent event/outbox error. There are no nested transactions and no global DB writes.

- Creation: authenticate/normalize, then in one transaction create the platform task, reserve quota/hold/snapshot, stage execution ciphertext, and write dispatch intent/event/outbox. `ReserveTx` verifies the Key belongs to Org and rereads the published price at creation time. Replayed task+request HMAC returns the existing snapshot before re-resolving changed pricing. Task IDs are globally unique platform IDs.
- Hold `ref_type=video`, idempotency key `video:<task_id>:hold:v1`. Existing `task` timeout sweep and generic hold sweep must not select this product. Old task code must not poll or settle `ark_official` tasks.
- Settlement: caller establishes a trustworthy generation outcome, saves the encrypted response evidence, calls `SettleUsageTx`, and updates task/event/outbox in the same transaction. `nil` completion tokens returns `pending_usage`, with no wallet or quota release. Explicit zero is preserved; callers must validate that zero is a credible official usage result before calling. Negative/overflow use is rejected. No estimate becomes actual usage.
- A source record/version identifies immutable usage evidence. A changed value under the same identity is `ErrConflict`. A later receipt that supplies missing usage must have a distinct source version/receipt identity. `EvidenceID` and `PayloadRef` point to caller-persisted official evidence, never to client assertions.
- `calculated_usd` is full retail charge; capture is at most the authorized hold. `absorbed_usd` records the difference; full actual tokens and supplier CNY/USD liability are retained. `QuotaOverrun` requires the orchestrator to stop affected new dispatch and raise review. The package never invokes the Ledger overage path or enables negative balance.
- Release requires `Confirmed=true`, an evidence ID and `not_dispatched` or `provider_no_charge`. This is an internal trusted API, never an HTTP-bound DTO. Confirmed means the caller has durable proof of no dispatch, or a verified provider terminal outcome with applicable no-charge policy. Timeout, unknown submit, missing usage, 404 and ambiguous DELETE 200+404 do not qualify. Supplier liability uncertainty must remain in recovery.
- Capture and release share `(org_id,task_id,settlement_key='initial')`. A different terminal outcome/changed actual usage cannot overwrite it; future customer adjustments need a separate authorized append-only flow. Supplier bill corrections cannot call customer debit automatically.
- Quote input contains verified integer width/height, rational fps, input-video duration and output duration OR frames. Adaptive/Draft/asset semantics belong to the normalizer. Set `BoundsVerified` only after establishing a defensible bound; caller-derived or changing URL duration is insufficient. The rule supplies minimum-token and platform risk versions; the package does not seed any commercial or official pricing assumptions.
- Input-video reservations must pass the in-process result of `QuoteWithMinimumTx`. It owns a private copy of the verified minimum selection domain; JSON/client round-trips or plain arithmetic `Quote` results cannot supply that proof. `ReserveTx` rereads the official-source evidence and every applicable cell in its transaction, then checks source ID/hash, minimum and all quoted amounts before writing Key/quota/Ledger state. Adaptive ratios and automatic durations retain the full domain (automatic geometric duration must equal its verified maximum), without assuming minima increase with duration. Exposed `Metering.MinimumTokens` is audit data, never the authority. Text-only reservations do not require minimum tables or this proof; accepted task replays return their original snapshot before current evidence/pricing checks.
- Money is USD `DECIMAL(20,8)`, rates `DECIMAL(24,12)`, CNY supplier amounts separate. Freeze uses ceiling to 8 decimals, final settlement uses decimal half-away-from-zero rounding to 8, then caps capture. Per-million scaling uses exact decimal shifts, never float or an intermediate rounded division. `fx_usd_per_cny` always means USD for 1 CNY. No FX refetch at settlement.

## Quota publication and locking

The existing `api_keys.quota_limit=0` convention remains unlimited; this package reserves `used_amount` under `used+hold<=limit` for finite limits. Return-to-actual runs in the same transaction. Legacy products still have their original soft-budget behavior; this package does not claim to fix those products. New `video_quota_policies` use `NULL=unlimited, 0=deny` explicitly.

Policies match Org, Key, Org+model and provider scope, with USD/CNY/token/task metrics and day/month/lifetime periods. CNY is provider-only. USD means customer capture for customer scopes and full calculated supplier cost for provider scopes. Task quotas count successful tasks; failed/no-charge release consumes zero. There is no concurrency-limit policy in this S1 package; the task scheduler owns concurrency.

All applicable enabled policies are enforced; there are no invented implicit supplier caps. Provision required supplier/day/month limits before enabling dispatch. Two active versions of the same logical quota dimension fail closed. Policy administration must serialize publication, enabling the new version and disabling the old atomically. Do not edit price-rule contents or snapshot rows after publication.

Buckets use `scope_key` (SHA-256 over a structured Org/scope/owner/model/metric/period identity) plus period start, **not policy version**. Publishing a limit change retains consumed/reserved totals. The current enabled policy provides the limit; a lower limit can deny further work without erasing old usage. Timezone changes after buckets exist are rejected and require an explicit audited migration. Changing budget dimensions is a new policy, not an automatic transfer of old usage. Creation-time buckets retain in-flight quota across day/month boundaries.

Lock order: owning Key row first, then quota buckets sorted by stable scope_key, then Ledger hold/account. Snapshot reads are scoped by Org; settlement locks the Key and snapshot before budgets. Orchestration must use a compatible order; transaction deadlocks must roll back and be retried using the platform idempotency record. Do not independently commit a quota reservation.

Token overrun preserves true units and blocks the affected bucket. This is admission against estimates, not a guarantee of an absolute upstream Token cap. Supplier budget moves from reserved to calculated consumed liability at usage settlement; later bill reconciliation must append authoritative cost adjustments, not reset consumed to zero.

## MySQL 8.4 DDL draft (not an applied migration)

The integration owner owns numbered migrations and schema-doctor updates. Tables below deliberately have no task/provider FK because those shared tables are owned by integration; add appropriate same-Org composite constraints when integrating. Existing api_keys and Ledger tables are reused. Customer table operations still enforce Org in their repository predicates. `metering_json` stores typed numeric/version metadata only, never prompts, URLs or credentials.

```sql
CREATE TABLE video_pricing_rules (
 id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
 rule_id VARCHAR(64) NOT NULL, version VARCHAR(64) NOT NULL,
 provider_scope_id BIGINT UNSIGNED NOT NULL, model VARCHAR(64) NOT NULL,
 mode VARCHAR(32) NOT NULL, resolution VARCHAR(32) NOT NULL,
 input_video BOOLEAN NOT NULL, service_tier VARCHAR(32) NOT NULL, draft BOOLEAN NOT NULL,
 retail_usd_per_1m DECIMAL(24,12) NOT NULL, supplier_cny_per_1m DECIMAL(24,12) NOT NULL,
 minimum_tokens BIGINT UNSIGNED NOT NULL, minimum_rule_version VARCHAR(64) NOT NULL,
 risk_multiplier DECIMAL(24,12) NOT NULL, formula_version VARCHAR(64) NOT NULL,
 source_hash CHAR(64) NOT NULL, effective_from DATETIME(3) NOT NULL,
 effective_until DATETIME(3) NULL, enabled BOOLEAN NOT NULL,
 UNIQUE KEY uk_video_price_version(rule_id,version),
 KEY idx_video_price_lookup(provider_scope_id,model,enabled,effective_from)
) ENGINE=InnoDB;

CREATE TABLE video_quota_policies (
 id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
 org_id BIGINT UNSIGNED NOT NULL, scope VARCHAR(16) NOT NULL,
 owner_id BIGINT UNSIGNED NOT NULL, model VARCHAR(64) NOT NULL,
 metric VARCHAR(16) NOT NULL, period VARCHAR(16) NOT NULL, timezone VARCHAR(64) NOT NULL,
 limit_amount DECIMAL(20,8) NULL, limit_units BIGINT UNSIGNED NULL,
 version VARCHAR(64) NOT NULL, enabled BOOLEAN NOT NULL,
 UNIQUE KEY uk_video_quota_policy(org_id,scope,owner_id,model,metric,period,version),
 KEY idx_video_quota_lookup(org_id,scope,owner_id,enabled)
) ENGINE=InnoDB;

CREATE TABLE video_quota_buckets (
 id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
 policy_id BIGINT UNSIGNED NOT NULL, scope_key CHAR(64) NOT NULL,
 timezone VARCHAR(64) NOT NULL, org_id BIGINT UNSIGNED NOT NULL,
 period_start DATETIME(3) NOT NULL, period_end DATETIME(3) NULL,
 limit_amount DECIMAL(20,8) NULL, limit_units BIGINT UNSIGNED NULL,
 reserved_amount DECIMAL(20,8) NOT NULL DEFAULT 0, consumed_amount DECIMAL(20,8) NOT NULL DEFAULT 0,
 reserved_units BIGINT UNSIGNED NOT NULL DEFAULT 0, consumed_units BIGINT UNSIGNED NOT NULL DEFAULT 0,
 blocked BOOLEAN NOT NULL DEFAULT FALSE,
 UNIQUE KEY uk_video_quota_window(scope_key,period_start),
 KEY idx_video_quota_policy(policy_id)
) ENGINE=InnoDB;

CREATE TABLE video_quota_reservations (
 id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
 org_id BIGINT UNSIGNED NOT NULL, task_id VARCHAR(64) NOT NULL,
 bucket_id BIGINT UNSIGNED NOT NULL, metric VARCHAR(16) NOT NULL, supplier BOOLEAN NOT NULL,
 reserved_amount DECIMAL(20,8) NOT NULL, reserved_units BIGINT UNSIGNED NOT NULL,
 actual_amount DECIMAL(20,8) NOT NULL, actual_units BIGINT UNSIGNED NOT NULL,
 state VARCHAR(16) NOT NULL, created_at DATETIME(3) NOT NULL, settled_at DATETIME(3) NULL,
 UNIQUE KEY uk_video_reservation(task_id,bucket_id),
 KEY idx_video_reservation_task(org_id,task_id),
 KEY idx_video_reservation_active(bucket_id,state)
) ENGINE=InnoDB;

CREATE TABLE video_billing_snapshots (
 id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
 org_id BIGINT UNSIGNED NOT NULL, api_key_id BIGINT UNSIGNED NOT NULL,
 task_id VARCHAR(64) NOT NULL, provider_scope_id BIGINT UNSIGNED NOT NULL,
 model VARCHAR(64) NOT NULL, rule_id VARCHAR(64) NOT NULL, rule_version VARCHAR(64) NOT NULL,
 formula_version VARCHAR(64) NOT NULL, minimum_rule_version VARCHAR(64) NOT NULL,
 capability_version VARCHAR(64) NOT NULL, request_hmac CHAR(64) NOT NULL,
 metering_json TEXT NOT NULL, hold_id BIGINT UNSIGNED NOT NULL,
 estimated_tokens BIGINT UNSIGNED NOT NULL, hold_usd DECIMAL(20,8) NOT NULL,
 retail_usd_per_1m DECIMAL(24,12) NOT NULL, supplier_cny_per_1m DECIMAL(24,12) NOT NULL,
 effective_ratio DECIMAL(24,12) NOT NULL, fx_usd_per_cny DECIMAL(24,12) NOT NULL,
 fx_version VARCHAR(64) NOT NULL, fx_observed_at DATETIME(3) NOT NULL,
 supplier_reserved_cny DECIMAL(20,8) NOT NULL, supplier_reserved_usd DECIMAL(20,8) NOT NULL,
 created_at DATETIME(3) NOT NULL,
 UNIQUE KEY uk_video_snapshot_task(org_id,task_id), UNIQUE KEY uk_video_snapshot_hold(hold_id)
) ENGINE=InnoDB;

CREATE TABLE video_usage_evidence (
 id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
 org_id BIGINT UNSIGNED NOT NULL, task_id VARCHAR(64) NOT NULL,
 evidence_id VARCHAR(64) NOT NULL, source VARCHAR(32) NOT NULL,
 source_record_id VARCHAR(128) NOT NULL, source_version VARCHAR(64) NOT NULL,
 completion_tokens BIGINT UNSIGNED NULL, payload_ref VARCHAR(128) NOT NULL,
 observed_at DATETIME(3) NOT NULL,
 UNIQUE KEY uk_video_evidence_id(evidence_id),
 UNIQUE KEY uk_video_evidence_source(org_id,task_id,source,source_record_id,source_version)
) ENGINE=InnoDB;

CREATE TABLE video_billing_entries (
 id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
 org_id BIGINT UNSIGNED NOT NULL, task_id VARCHAR(64) NOT NULL,
 settlement_key VARCHAR(64) NOT NULL, state VARCHAR(32) NOT NULL,
 snapshot_id BIGINT UNSIGNED NOT NULL, evidence_id VARCHAR(64) NOT NULL,
 hold_id BIGINT UNSIGNED NOT NULL, journal_id BIGINT UNSIGNED NULL,
 completion_tokens BIGINT UNSIGNED NULL,
 calculated_usd DECIMAL(20,8) NOT NULL, captured_usd DECIMAL(20,8) NOT NULL,
 released_usd DECIMAL(20,8) NOT NULL, absorbed_usd DECIMAL(20,8) NOT NULL,
 supplier_cny DECIMAL(20,8) NOT NULL, supplier_usd DECIMAL(20,8) NOT NULL,
 reason VARCHAR(64) NOT NULL, created_at DATETIME(3) NOT NULL,
 UNIQUE KEY uk_video_initial_settlement(org_id,task_id,settlement_key),
 UNIQUE KEY uk_video_billing_journal(journal_id)
) ENGINE=InnoDB;
```

Use binary/case-sensitive collation for ID/hash/version columns in the final migration; no NULL loopholes in business unique keys. Tokens use nonnegative Go int64 even where storage is BIGINT UNSIGNED; reject values above MaxInt64 before parsing/calling. Pricing and policy publication, period migrations, trusted evidence capture and alert handling are integration responsibilities, not implicit behavior of `tx.Create`.

## Verification

`go test ./core/internal/videobilling -count=1` uses handwritten SQLite DDL and asserts wallet/journal/quota database outcomes, including later caller failure, injected bill insertion failure, missing/zero usage, multiple budget dimensions, cross-period settlement, policy revisions, conflicting evidence, replay, and no over-hold customer debit. SQLite tests use one connection and validate serialized correctness, not MySQL row-lock concurrency. Real MySQL concurrent admission/settlement and formal migration replay remain integration gates.

No real paid Ark task is performed by this package or its tests. The fixture price is a test selling price, not a published commercial quote. S2 tests add interrupted imports, duplicate/conflicting revisions, aggregate quarantine, verified binding recovery, pending usage, independent reviews, stale proposals, tenant isolation, capped debit/refund, supplier-only corrections and atomic rollback.

## S2 import, recovery and review contract

```go
BeginImportTx(tx *gorm.DB, in BeginImportInput) (BeginImportResult, error)
ImportLineTx(tx *gorm.DB, in ImportLineInput) (ImportLineResult, error)
CompleteImportTx(tx *gorm.DB, importID uint64, now time.Time) (BillImport, error)
BindBillLineTx(tx *gorm.DB, lineID uint64, binding VerifiedBinding, now time.Time) (ReconciliationItem, error)
SettleBillUsageTx(tx *gorm.DB, org uint64, task string, lineID uint64, now time.Time) (SettlementResult, error)
ProposeAdjustmentTx(tx *gorm.DB, in ProposeAdjustmentInput) (AdjustmentProposal, error)
ReviewAdjustmentTx(tx *gorm.DB, in ReviewAdjustmentInput) (AdjustmentReview, error)
ApplyAdjustmentTx(tx *gorm.DB, in ApplyAdjustmentInput) (AdjustmentResult, error)
```

These remain **internal trusted APIs**, with no network/server dependency. The root service owns RBAC, strict JSON decoding, tenant authorization, encrypted payload ingestion, provider credential/account scope, actor identities, task state CAS and audit/event/outbox writes. In particular, a bill column claiming an Org or task is never a verified binding. Resolve `(provider_scope_id, upstream_task_id)` using the root's unique upstream binding table within the same transaction. `VerifiedBinding` is assembled server-side after checking its Org, task and provider scope. Source record IDs and safe version metadata are retained; raw bill files and rows are encrypted by Invocation Store, referenced only by opaque `payload_ref`. Do not store raw rows, URLs or credentials in free-text reasons, logs or these tables.

1. Verify supplier source/authenticity and currency, store encrypted bill bytes, compute `source_hash`, then `BeginImportTx`. `ImportKey` identifies one immutable export; changing hash/version/row count under that key is a conflict. Different receipt keys may reference the same supplier line without duplicating that line.
2. Import stable 1-based row ordinals in small transactions. Row and line evidence commit together. Retry the original ordinal after a crash; same ordinal with different content conflicts. A source line identity is `(provider_scope_id, source, source_record_id, source_version)`; same identity/different canonical content conflicts. Same numeric revision with a different version also conflicts. `content_hash` covers typed numerical/identity fields and the verified `evidence_hash`; retrieval time/ciphertext rotation do not change source identity.
3. `SourceRevision` must be a **verified monotonic revision order within one source record**, not lexical comparison of version strings, ingestion order or a fabricated counter. The importer must prove the source's supersession relation. If it cannot, hold the evidence in the external import quarantine for manual determination. Older revisions append as stale evidence; they never replace `video_provider_bill_heads`. A series cannot change upstream task/model/granularity or previously verified tenant mapping. A new supplier record is not automatically another revision of the same task total.
4. `task_total` means one authoritative complete task charge, in CNY, and nullable completion tokens, for that specific upstream task. Fees/taxes/credits must follow the verified supplier billing convention. Individual charge components or negative credit rows cannot be mislabeled task totals. Aggregate signed cost rows can be imported as `aggregate`, with empty upstream task ID, and remain quarantined; no proportional task-level allocation is made. For task totals, CNY must be nonnegative. All token values must fit nonnegative int64; amounts are exact decimal strings at the admin boundary.
5. The import only appends evidence and difference observations. It never refunds, releases, captures or adjusts budgets. Unresolved exact rows remain quarantined. Later `BindBillLineTx` appends one binding plus a new reconciliation item, leaving the original quarantined observation intact. Binding to two tenants/tasks fails. `CompleteImportTx` requires all declared ordinals; count is not a cursor. A blocked/conflicting row is resolved explicitly rather than silently skipped.
6. `SettleBillUsageTx` is an **explicit recovery action** after root has established trustworthy official billing facts and the task binding. It is never called by import. Missing completion tokens returns `pending_usage` and keeps every hold/reservation. Exact zero is distinguishable from missing; root must verify the zero record's meaning. This uses the original token price/FX snapshot and capped initial settlement; supplier invoice cost discrepancies remain separate. Unknown submit, timeout, DELETE 200+404, and aggregate invoices do not justify release. An already settled conflicting token count must enter the reviewed correction flow.
7. `ProposeAdjustmentTx` requires a completed initial settlement/release and the current exact source revision. It captures the current application ID/net charge, proposed amounts/tokens, proposer and expiry. `SupplierOnly=true` can correct supplier CNY/USD and factual token usage with **zero customer delta**; missing bill tokens retain the existing factual tokens. `SupplierOnly=false` requires explicit bill tokens and proposes retail correction using the creation-time sales price and multiplier. Supplier CNY changes never supply a retail price. Keep `supplier_only` explicit at HTTP boundaries; omission should be rejected or default to true there.
8. A different authenticated reviewer records one immutable approve/reject decision. The package rejects self-review and exposes no bypass. Root must enforce its approval role/permission, actor audit and organization visibility. Apply requires approval, unexpired review artifact, unchanged current application ID and still-current source revision. Fresh supplier evidence makes an older proposal stale. Rejected/expired/stale artifacts remain in history; create a new proposal/key. A repeated application returns its original outcome even after later corrections.
9. Apply appends a journal entry for the signed customer delta (positive delta means debit), plus an immutable application. Customer net charge stays in `[0, original_hold]`; the remaining theoretical charge stays platform-absorbed. Negative wallet balance and spending other active holds are forbidden. Debit checks Key and original customer USD buckets; if funds/quota are insufficient, the whole transaction rolls back and the discrepancy remains open for review. Posting goes to the original hold's account shard; shard rebalance is an external, separately authorized operation if needed.
10. Budget bucket totals are projections: customer USD follows actual net charge; supplier USD/CNY follow accepted invoice liability using original FX; token totals follow accepted real tokens. Corrections apply deltas to the **creation-time buckets**, preserving cross-period attribution. Reservations and initial billing entries stay unchanged; applications explain every projection delta. Real supplier/token excess is retained and blocks further admission, never clipped to the estimate. A downward correction does not automatically unblock a risk bucket. Task-count quotas continue to follow the trusted generation outcome; invoice corrections do not infer task status or change task-count consumption.

Lock order is compatible with S1: task orchestration's own locks, owning Key/snapshot, bill head, quota buckets in stable scope order, then Ledger account. Import locks its import row and bill head but does not take Key/quota/ledger locks. Review only appends one decision. `BindBillLineTx` and `ImportLineTx` do not settle; do not compose a prior import/head lock followed by a new task/Key lock in the same transaction. Import first, commit evidence, then run explicit recovery or correction in its own transaction with task/outbox/audit.

Only import progress, bill-head projections and quota totals are updated. Bill lines, import-row receipts, bindings, reconciliation observations, proposals, reviews, applications and Ledger journal are append-only. `ReconciliationItem` records a historical comparison against the initial settlement, including its entry ID; it is not a mutable final task balance. Current financial position is initial entry followed by the latest application. Root can show unresolved/stale/reviewed status by joining those facts without overwriting evidence. Different source-record series cannot be substituted after an application; conflicting supplier identifiers require explicit reconciliation outside this automated correction path.

## Admin JSON mapping (root-owned routes)

No admin handler is registered by this package. Suggested strict request bodies below map directly to exported input types; path parameters carry internal IDs. Root injects `now`, authenticated `proposer_id`/`reviewer_id`, verified scope/Org bindings, and an audit request ID. Use decimal strings, preserve integer tokens, reject unknown fields and explicit negative/overflow tokens. Reject public clients entirely.

```json
{"import_key":"bill-202610-v1","provider_scope_id":11,"source":"official-bill","source_version":"export-v1","source_hash":"<64 hex>","payload_ref":"<encrypted object ref>","expected_rows":125}
```

For `POST /admin/video/bill-imports/{import_id}/lines`, root resolves the binding from `provider_scope_id` plus `upstream_task_id`; **no binding/Org/task is accepted from the bill body**. A validated line body is:

```json
{"row_number":1,"provider_scope_id":11,"source":"official-bill","source_record_id":"supplier-row-id","source_version":"revision-1","source_revision":1,"evidence_hash":"<64 hex>","payload_ref":"<encrypted row ref>","granularity":"task_total","upstream_task_id":"provider-task-id","model":"seedance-2.0","period_start":"2026-10-01T00:00:00Z","period_end":"2026-11-01T00:00:00Z","completion_tokens":54000,"amount_cny":"2.48400000","observed_at":"2026-11-02T00:00:00Z"}
```

`completion_tokens:null` is allowed. `POST .../{import_id}/complete` needs an empty body. A binding recovery endpoint takes only a `line_id`; root obtains `VerifiedBinding`. A recovery settlement endpoint takes authorized Org/task path parameters plus `{ "line_id": 1 }` and calls `SettleBillUsageTx` only after the task outcome verification described above.

```json
{"proposal_key":"adjustment-1","line_id":1,"reason":"verified_invoice_correction","supplier_only":true,"expires_at":"2026-11-03T00:00:00Z"}
```

For proposal creation root takes Org/task from authorized path/context. Review body: `{ "approved": true, "reason": "verified_bill" }`; actor is authenticated context, never JSON. Apply body is empty; root supplies Org/proposal ID and includes audit/event/outbox in the transaction. Classify `ErrUnmapped`, `ErrNotSettled`, `ErrReviewRequired`, `ErrStale`, `ErrConflict` and quota/funds errors explicitly. Do not parse error text or treat an adjustment failure as evidence to refund the original task.

## S2 MySQL DDL draft (not an applied migration)

All identifiers/hashes/version strings require binary case-sensitive collation in the numbered root migration. Monetary delta columns are signed `DECIMAL(20,8)`, completion tokens nonnegative nullable BIGINT, token deltas signed BIGINT. Add root-owned same-Org task/provider foreign keys where compatible; do not use `AutoMigrate`. `source_revision` is capped to positive int64 in Go. The nine additional tables are:

```sql
CREATE TABLE video_provider_bill_imports (
 id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
 import_key VARCHAR(64) NOT NULL, provider_scope_id BIGINT UNSIGNED NOT NULL,
 source VARCHAR(64) NOT NULL, source_version VARCHAR(64) NOT NULL,
 source_hash CHAR(64) NOT NULL, payload_ref VARCHAR(128) NOT NULL,
 expected_rows BIGINT NOT NULL, state VARCHAR(16) NOT NULL,
 created_at DATETIME(3) NOT NULL, completed_at DATETIME(3) NULL,
 UNIQUE KEY uk_video_bill_import(provider_scope_id,import_key),
 KEY idx_video_bill_import_progress(state,created_at)
) ENGINE=InnoDB;

CREATE TABLE video_provider_bill_lines (
 id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
 provider_scope_id BIGINT UNSIGNED NOT NULL, source VARCHAR(64) NOT NULL,
 source_record_id VARCHAR(128) NOT NULL, source_version VARCHAR(64) NOT NULL,
 source_revision BIGINT NOT NULL, content_hash CHAR(64) NOT NULL,
 evidence_hash CHAR(64) NOT NULL, payload_ref VARCHAR(128) NOT NULL,
 granularity VARCHAR(16) NOT NULL, upstream_task_id VARCHAR(128) NOT NULL,
 model VARCHAR(64) NOT NULL, period_start DATETIME(3) NOT NULL,
 period_end DATETIME(3) NOT NULL, completion_tokens BIGINT UNSIGNED NULL,
 amount_cny DECIMAL(20,8) NOT NULL, observed_at DATETIME(3) NOT NULL,
 UNIQUE KEY uk_video_bill_version(provider_scope_id,source,source_record_id,source_version),
 UNIQUE KEY uk_video_bill_revision(provider_scope_id,source,source_record_id,source_revision),
 KEY idx_video_bill_upstream(provider_scope_id,upstream_task_id),
 KEY idx_video_bill_period(provider_scope_id,period_start)
) ENGINE=InnoDB;

CREATE TABLE video_provider_bill_import_rows (
 id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
 import_id BIGINT UNSIGNED NOT NULL, source_ordinal BIGINT NOT NULL,
 line_id BIGINT UNSIGNED NOT NULL, content_hash CHAR(64) NOT NULL,
 UNIQUE KEY uk_video_bill_import_row(import_id,source_ordinal),
 KEY idx_video_bill_receipt_line(line_id)
) ENGINE=InnoDB;

CREATE TABLE video_provider_bill_heads (
 id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
 provider_scope_id BIGINT UNSIGNED NOT NULL, source VARCHAR(64) NOT NULL,
 source_record_id VARCHAR(128) NOT NULL, source_revision BIGINT NOT NULL,
 line_id BIGINT UNSIGNED NOT NULL,
 UNIQUE KEY uk_video_bill_head(provider_scope_id,source,source_record_id)
) ENGINE=InnoDB;

CREATE TABLE video_provider_bill_bindings (
 line_id BIGINT UNSIGNED NOT NULL PRIMARY KEY, org_id BIGINT UNSIGNED NOT NULL,
 task_id VARCHAR(64) NOT NULL, binding_version VARCHAR(64) NOT NULL,
 created_at DATETIME(3) NOT NULL,
 KEY idx_video_bill_binding_task(org_id,task_id)
) ENGINE=InnoDB;

CREATE TABLE video_reconciliation_items (
 id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
 line_id BIGINT UNSIGNED NOT NULL, org_id BIGINT UNSIGNED NOT NULL,
 task_id VARCHAR(64) NOT NULL, binding_version VARCHAR(64) NOT NULL,
 state VARCHAR(32) NOT NULL, reason VARCHAR(64) NOT NULL,
 initial_entry_id BIGINT UNSIGNED NULL,
 observed_tokens BIGINT UNSIGNED NULL, expected_tokens BIGINT UNSIGNED NULL,
 observed_cny DECIMAL(20,8) NOT NULL, expected_cny DECIMAL(20,8) NOT NULL,
 difference_cny DECIMAL(20,8) NOT NULL, created_at DATETIME(3) NOT NULL,
 UNIQUE KEY uk_video_reconciliation_binding(line_id,binding_version),
 KEY idx_video_reconciliation_task(org_id,task_id,id),
 KEY idx_video_reconciliation_queue(state,id)
) ENGINE=InnoDB;

CREATE TABLE video_adjustment_proposals (
 id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
 proposal_key VARCHAR(64) NOT NULL, org_id BIGINT UNSIGNED NOT NULL,
 task_id VARCHAR(64) NOT NULL, line_id BIGINT UNSIGNED NOT NULL,
 proposer_id BIGINT UNSIGNED NOT NULL, reason VARCHAR(64) NOT NULL,
 supplier_only BOOLEAN NOT NULL, base_application_id BIGINT UNSIGNED NOT NULL,
 base_net_usd DECIMAL(20,8) NOT NULL, target_net_usd DECIMAL(20,8) NOT NULL,
 target_calculated_usd DECIMAL(20,8) NOT NULL, target_cny DECIMAL(20,8) NOT NULL,
 target_supplier_usd DECIMAL(20,8) NOT NULL, target_tokens BIGINT UNSIGNED NULL,
 created_at DATETIME(3) NOT NULL, expires_at DATETIME(3) NOT NULL,
 UNIQUE KEY uk_video_adjustment_key(org_id,proposal_key),
 KEY idx_video_adjustment_task(org_id,task_id,id)
) ENGINE=InnoDB;

CREATE TABLE video_adjustment_reviews (
 id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
 proposal_id BIGINT UNSIGNED NOT NULL, org_id BIGINT UNSIGNED NOT NULL,
 reviewer_id BIGINT UNSIGNED NOT NULL, approved BOOLEAN NOT NULL,
 reason VARCHAR(64) NOT NULL, created_at DATETIME(3) NOT NULL,
 UNIQUE KEY uk_video_adjustment_review(proposal_id)
) ENGINE=InnoDB;

CREATE TABLE video_adjustment_applications (
 id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
 proposal_id BIGINT UNSIGNED NOT NULL, review_id BIGINT UNSIGNED NOT NULL,
 org_id BIGINT UNSIGNED NOT NULL, task_id VARCHAR(64) NOT NULL,
 line_id BIGINT UNSIGNED NOT NULL, journal_id BIGINT UNSIGNED NULL,
 customer_delta_usd DECIMAL(20,8) NOT NULL,
 supplier_delta_cny DECIMAL(20,8) NOT NULL, supplier_delta_usd DECIMAL(20,8) NOT NULL,
 tokens_delta BIGINT NOT NULL, net_usd DECIMAL(20,8) NOT NULL,
 calculated_usd DECIMAL(20,8) NOT NULL, absorbed_usd DECIMAL(20,8) NOT NULL,
 supplier_cny DECIMAL(20,8) NOT NULL, supplier_usd DECIMAL(20,8) NOT NULL,
 completion_tokens BIGINT UNSIGNED NULL, created_at DATETIME(3) NOT NULL,
 UNIQUE KEY uk_video_adjustment_application(proposal_id),
 UNIQUE KEY uk_video_adjustment_journal(journal_id),
 KEY idx_video_adjustment_position(org_id,task_id,id)
) ENGINE=InnoDB;
```

Import-head rows use `line_id=0,source_revision=0` only transiently in the same transaction before the first evidence append, so a strict line FK needs to accommodate that creation pattern or the root migration must use an appropriate nullable projection adaptation. The package rejects nontransaction DB handles. All public APIs require rollback on any error, including uniqueness conflicts; a caller can retry from a fresh transaction with the original immutable request after inspecting its persisted receipt.
