-- Ark console pricing evidence and auditable fund activity.
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
