# Video billing: S1 transaction contract

Implemented here: exact decimal quote from verified metering bounds, immutable task price snapshots, hard Key quota plus multi-dimensional budget reservations, wallet hold/capture/release, nullable real usage evidence, one initial settlement, and rollback-safe accounting. This package performs no HTTP, task lifecycle changes, migrations, event/outbox writes, or supplier bill import. Those responsibilities belong to orchestration and later delivery stages.

## Integration

```go
Quote(rule PricingRule, in MeteringInput) (QuoteResult, error)
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

No real paid Ark task is performed by this package or its tests. The fixture price is a test selling price, not a published commercial quote. Supplier bill import and cost revisions are the next work package; no exact upstream bill matching is claimed by this S1 implementation.
