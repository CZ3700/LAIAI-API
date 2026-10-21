-- Ark console pricing evidence and auditable fund activity.
CREATE TABLE ledger_operation_events (
 id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
 operation_id BIGINT UNSIGNED NOT NULL, status VARCHAR(16) NOT NULL,
 code VARCHAR(64) NOT NULL DEFAULT '', created_at DATETIME(3) NOT NULL,
 KEY idx_ledger_operation_event (operation_id,id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
