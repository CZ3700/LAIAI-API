CREATE TABLE video_credential_snapshots (
 id BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY, org_id BIGINT UNSIGNED NOT NULL,
 task_id VARCHAR(64) NOT NULL, provider_scope_id BIGINT UNSIGNED NOT NULL,
 source_credential_id BIGINT UNSIGNED NOT NULL, ciphertext TEXT NOT NULL, created_at DATETIME(3) NOT NULL,
 UNIQUE KEY uk_video_credential_task(org_id,task_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
