# Ark console billing integration

Register `s.registerArkPricingConsole(su)` in the existing individual-superadmin group; register `s.registerFundActivities(org)` in the authenticated org group. UI paths `/app/video-pricing` and `/app/fund-activities`.

Before constructing ANY service in core and relay, use `db, err = ledger.EnableActivityTracking(db)`; startup must fail on error. The GORM setting survives WithContext/Transaction clones. Runtime movement persistence errors roll back the financial transaction. Production must never use an untracked DB. Legacy test fixtures explicitly choose whether tracking is enabled; the dedicated activity tests require it.

For actual failed/processing attempts, wrap the existing external operation boundary with `ledger.RunOperation(db, ledger.OperationIntent{OrgID, IdempotencyKey, Type, Direction, RequestedAmount, RefType, RefID}, func(tx *gorm.DB) error { ... existing Ledger work ... })`. Replayed processing never retries money automatically; callback errors become failed only after confirmed rollback. A successful journal cannot be relabeled as a failed attempt. Do not nest RunOperation in an existing transaction or put network calls inside it.

Admission must replace `videobilling.Quote(rule,meter)` with `videobilling.QuoteWithMinimumTx(tx,rule,meter,videobilling.MinimumContext{Resolution:effective.Resolution,Ratio:effective.Ratio,Ratios:cap.Ratios,MinDuration:cap.MinDuration,MaxDuration:cap.MaxDuration,AutomaticDuration:effective.Duration == -1})`. Reference video admission fails closed when any needed official minimum cell is absent. The input amount used is the existing verified conservative upper bound. Snapshot MeteringJSON now includes dimensions and selected minimum evidence, so history never reprices.

The old `/video/configurations` publishing route must call the same minimum coverage validation before enabling an input-video rule; there is no scalar floor fallback in the new admission helper. Existing configuration route cannot manufacture a minimum-rule set.

## DDL draft (root assigns migration number)

```sql
CREATE TABLE video_minimum_rule_sets (
 id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
 model VARCHAR(64) NOT NULL, version VARCHAR(64) NOT NULL,
 source_url VARCHAR(1024) NOT NULL, evidence_hash CHAR(64) NOT NULL,
 observed_at DATETIME(3) NOT NULL, verified_by BIGINT UNSIGNED NOT NULL,
 verified_at DATETIME(3) NOT NULL,
 UNIQUE KEY uk_video_minimum_version (model,version)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE video_minimum_rule_cells (
 id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
 rule_set_id BIGINT UNSIGNED NOT NULL, resolution VARCHAR(16) NOT NULL,
 ratio VARCHAR(16) NOT NULL, output_duration_ms BIGINT NOT NULL,
 fps BIGINT NOT NULL, minimum_tokens BIGINT NOT NULL,
 UNIQUE KEY uk_video_minimum_cell (rule_set_id,resolution,ratio,output_duration_ms,fps)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE video_pricing_publications (
 id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
 model VARCHAR(64) NOT NULL, version VARCHAR(64) NOT NULL,
 config_version VARCHAR(64) NOT NULL, idempotency_key VARCHAR(128) NOT NULL,
 body_hash CHAR(64) NOT NULL, snapshot_json MEDIUMTEXT NOT NULL,
 created_by BIGINT UNSIGNED NOT NULL, created_at DATETIME(3) NOT NULL,
 UNIQUE KEY uk_video_pricing_publication (model,version),
 UNIQUE KEY uk_video_pricing_request (created_by,idempotency_key)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE ledger_operations (
 id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
 org_id BIGINT UNSIGNED NOT NULL, idempotency_key VARCHAR(160) NOT NULL,
 request_hash VARCHAR(64) NOT NULL DEFAULT '', type VARCHAR(64) NOT NULL,
 direction VARCHAR(16) NOT NULL, requested_amount DECIMAL(20,8) NOT NULL,
 currency CHAR(3) NOT NULL, status VARCHAR(16) NOT NULL,
 ref_type VARCHAR(64) NOT NULL, ref_id VARCHAR(160) NOT NULL,
 failure_code VARCHAR(64) NOT NULL DEFAULT '', created_at DATETIME(3) NOT NULL,
 updated_at DATETIME(3) NOT NULL,
 UNIQUE KEY uk_ledger_operation (org_id,idempotency_key),
 KEY idx_ledger_operation_org (org_id,id),
 KEY idx_ledger_operation_status (org_id,status,id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE ledger_operation_events (
 id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
 operation_id BIGINT UNSIGNED NOT NULL, status VARCHAR(16) NOT NULL,
 code VARCHAR(64) NOT NULL DEFAULT '', created_at DATETIME(3) NOT NULL,
 KEY idx_ledger_operation_event (operation_id,id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE TABLE ledger_movements (
 id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
 org_id BIGINT UNSIGNED NOT NULL, account_id BIGINT UNSIGNED NOT NULL,
 account_type VARCHAR(32) NOT NULL, operation_id BIGINT UNSIGNED NOT NULL,
 type VARCHAR(64) NOT NULL, direction VARCHAR(16) NOT NULL,
 status VARCHAR(16) NOT NULL, currency CHAR(3) NOT NULL,
 amount DECIMAL(20,8) NOT NULL, posted_delta DECIMAL(20,8) NOT NULL,
 held_delta DECIMAL(20,8) NOT NULL, available_delta DECIMAL(20,8) NOT NULL,
 journal_id BIGINT UNSIGNED NULL, hold_id BIGINT UNSIGNED NULL,
 source_type VARCHAR(32) NOT NULL, source_id BIGINT UNSIGNED NOT NULL,
 component VARCHAR(32) NOT NULL, ref_type VARCHAR(64) NOT NULL,
 ref_id VARCHAR(160) NOT NULL, quality VARCHAR(32) NOT NULL,
 created_at DATETIME(3) NOT NULL,
 UNIQUE KEY uk_ledger_movement_source (source_type,source_id,component),
 KEY idx_ledger_movement_org (org_id,id),
 KEY idx_ledger_movement_operation (operation_id,org_id),
 KEY idx_ledger_movement_filter (org_id,direction,created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
```

No commercial price or minimum-token value is seeded. The full official minimum table currently requires operator access; source URL, original evidence SHA-256, observation date and authenticated reviewer are mandatory. Operator verification is an assertion recorded for audit, not automatic verification of official authenticity.

## Incremental movement provenance DDL (0116)

```sql
ALTER TABLE ledger_movements
 ADD COLUMN original_movement_id BIGINT UNSIGNED NULL,
 ADD COLUMN original_journal_id BIGINT UNSIGNED NULL,
 ADD COLUMN original_hold_id BIGINT UNSIGNED NULL,
 ADD COLUMN correction_proposal_id BIGINT UNSIGNED NULL,
 ADD COLUMN correction_review_id BIGINT UNSIGNED NULL,
 ADD COLUMN occurred_at DATETIME(3) NULL,
 ADD KEY idx_ledger_movement_occurred (org_id,occurred_at,id);
```

`reversed` labels a new reviewed corrective posting (refund or additional charge), never a rewritten original entry or an assertion of full cancellation. ApplyAdjustmentTx supplies the actual two-person proposal/review/hold links. Live movements have occurred_at; historical journal/hold creation uses source time, release inferred from state has NULL occurred_at and `derived_time_unknown` quality. Date-filtered sums use occurred_at, so unknown historical release time cannot become today's activity. created_at is ingestion time only. No org balance_after is fabricated.

`ledger.BackfillActivities(db, cutoffUTC, limit)` is a bounded operator job (limit 1–500) using source uniqueness also used by live writes. Repeat until 0. It never writes balances or journal. Every inserted movement is in one transaction with its operation. Historical failed attempts cannot be recovered from successful journal and are never synthesized.

## Durable export DDL (0117)

```sql
CREATE TABLE ledger_export_jobs (
 id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
 org_id BIGINT UNSIGNED NOT NULL, user_id BIGINT UNSIGNED NOT NULL,
 idempotency_key VARCHAR(128) NOT NULL, request_hash CHAR(64) NOT NULL,
 filter_json TEXT NOT NULL, max_movement_id BIGINT UNSIGNED NOT NULL,
 cursor_id BIGINT UNSIGNED NOT NULL DEFAULT 0, row_count INT NOT NULL DEFAULT 0,
 status VARCHAR(16) NOT NULL, error_code VARCHAR(64) NOT NULL DEFAULT '',
 csv_data MEDIUMTEXT NOT NULL, created_at DATETIME(3) NOT NULL,
 expires_at DATETIME(3) NOT NULL, updated_at DATETIME(3) NOT NULL,
 UNIQUE KEY uk_fund_export_request (org_id,user_id,idempotency_key),
 KEY idx_fund_export_work (status,expires_at,id),
 KEY idx_fund_export_owner (org_id,user_id,id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
```

Launch `go srv.RunFundExportJobs(shutdownContext)` once after constructing server. Each SQL-only chunk serializes on the job row, so multiple replicas are safe. A restarted worker resumes the saved cursor; CSV and cursor commit together. POST `/api/fund-exports` accepts the list filters and Idempotency-Key; GET `/:id` returns status; GET `/:id/download` returns authenticated JSON `{filename,csv,rows,currency,snapshot_at}`. Register in the active-membership org group, never a bare token group. Owner user + org + 15-minute TTL are checked again on every fetch; create/download are audited. CSV contents are cleared after expiration. Max 100,000 rows / 14 MiB fails explicitly instead of silently truncating. Ordinary GET `/fund-exports` remains a bounded compatibility preview, not the full export job.

Movement pagination is actual occurred_at DESC, id DESC, NULL time last. `before` remains a movement ID; the repository resolves the same-org anchor and uses its timestamp in a compound keyset predicate. Unknown historical timestamps are never classified as today's activity. Export also fixes max_movement_id, so later live or backfill inserts are excluded.
