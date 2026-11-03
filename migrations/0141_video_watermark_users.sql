-- Per-user watermark rollout. An empty allowlist means no user is eligible.
-- Organization-level API keys have no user_id and remain outside this feature.
ALTER TABLE video_watermark_jobs
 ADD COLUMN user_id BIGINT UNSIGNED NULL DEFAULT NULL AFTER org_id,
 ADD KEY idx_video_watermark_user (user_id, state);

CREATE TABLE video_watermark_users (
 user_id BIGINT UNSIGNED PRIMARY KEY,
 granted_by BIGINT UNSIGNED NOT NULL,
 granted_at DATETIME(3) NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
