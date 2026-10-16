-- Ark S2: durable video_adjustment_proposals
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
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
