-- Ark video: durable video_quota_reservations
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
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
