-- Safe sync summaries only; raw pages and cursors stay in Invocation Store.
ALTER TABLE video_asset_sync_jobs
 ADD COLUMN request_id VARCHAR(64) NOT NULL DEFAULT '',
 ADD COLUMN http_status INT NOT NULL DEFAULT 0,
 ADD COLUMN upstream_request_id VARCHAR(128) NOT NULL DEFAULT '',
 ADD COLUMN attempts BIGINT UNSIGNED NOT NULL DEFAULT 0,
 ADD COLUMN retry_count BIGINT UNSIGNED NOT NULL DEFAULT 0,
 ADD COLUMN next_attempt_at DATETIME(3) NULL,
 ADD COLUMN last_request_payload_id VARCHAR(64) NOT NULL DEFAULT '',
 ADD COLUMN last_response_payload_id VARCHAR(64) NOT NULL DEFAULT '';
