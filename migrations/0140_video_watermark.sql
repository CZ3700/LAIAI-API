-- Watermark-removal delivery pipeline. Expand-only.
-- video_watermark_jobs: one row per succeeded Ark task, registered inside the
-- trusted-succeeded transaction and consumed by the watermark worker. The
-- upstream signed URL is resolved fresh at download time, so only the immutable
-- task identity is stored here; the processed artifact lives in MediaStore.
-- video_watermark_settings: singleton superadmin toggle (id=1), default off.
CREATE TABLE video_watermark_jobs (
 id BIGINT UNSIGNED PRIMARY KEY AUTO_INCREMENT,
 org_id BIGINT UNSIGNED NOT NULL,
 task_id VARCHAR(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL,
 state VARCHAR(16) NOT NULL DEFAULT 'pending',
 attempts INT NOT NULL DEFAULT 0,
 lease_until DATETIME(3) NULL DEFAULT NULL,
 processed_media_id VARCHAR(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NULL DEFAULT NULL,
 processed_expires_at DATETIME(3) NULL DEFAULT NULL,
 last_error VARCHAR(512) NULL DEFAULT NULL,
 created_at DATETIME(3) NOT NULL,
 updated_at DATETIME(3) NOT NULL,
 UNIQUE KEY uq_video_watermark_task (task_id),
 KEY idx_video_watermark_due (state, lease_until)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE video_watermark_settings (
 id BIGINT UNSIGNED PRIMARY KEY,
 enabled TINYINT(1) NOT NULL DEFAULT 0,
 updated_by BIGINT UNSIGNED NOT NULL DEFAULT 0,
 updated_at DATETIME(3) NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

INSERT INTO video_watermark_settings (id, enabled, updated_by, updated_at)
 VALUES (1, 0, 0, UTC_TIMESTAMP(3));
