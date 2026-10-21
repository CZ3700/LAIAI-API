-- Ark S2: durable video_adjustment_applications
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
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
