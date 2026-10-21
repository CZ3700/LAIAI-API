-- Ark S2: durable video_reconciliation_items
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
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
