-- Reviewed corrections append evidence links; original records stay immutable.
-- occurred_at is NULL when the historical source cannot prove the exact time.
ALTER TABLE ledger_movements
 ADD COLUMN original_movement_id BIGINT UNSIGNED NULL,
 ADD COLUMN original_journal_id BIGINT UNSIGNED NULL,
 ADD COLUMN original_hold_id BIGINT UNSIGNED NULL,
 ADD COLUMN correction_proposal_id BIGINT UNSIGNED NULL,
 ADD COLUMN correction_review_id BIGINT UNSIGNED NULL,
 ADD COLUMN occurred_at DATETIME(3) NULL,
 ADD KEY idx_ledger_movement_occurred (org_id,occurred_at,id);
