-- Ark video: durable video_billing_entries
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
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
