-- Callback delivery is a durable trigger. One row per task permits an atomic
-- upsert while revision fencing keeps an in-flight consumer from acknowledging
-- a newer notification. 0150 removed historical duplicates before this index.
ALTER TABLE video_callback_hints
 ADD COLUMN revision BIGINT UNSIGNED NOT NULL DEFAULT 1,
 ADD COLUMN retry_at DATETIME(3) NULL,
 ADD COLUMN attempts INT UNSIGNED NOT NULL DEFAULT 0,
 ADD UNIQUE KEY uk_video_callback_task (task_id),
 ADD KEY idx_video_callback_retry (processed_at, retry_at, id);
