-- Ark video: durable api_request_logs
CREATE TABLE api_request_logs (
 request_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
 trace_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL DEFAULT '',
 org_id BIGINT UNSIGNED NULL, user_id BIGINT UNSIGNED NULL, api_key_id BIGINT UNSIGNED NULL,
 auth_outcome VARCHAR(16) NOT NULL, method VARCHAR(8) NOT NULL,
 route VARCHAR(160) NOT NULL, operation VARCHAR(64) NOT NULL,
 resource_type VARCHAR(64) NOT NULL DEFAULT '', resource_id VARCHAR(64) NOT NULL DEFAULT '',
 request_bytes BIGINT NOT NULL DEFAULT 0, request_payload_id VARCHAR(64) NOT NULL DEFAULT '',
 started_at DATETIME(3) NOT NULL,
 http_status INT NOT NULL DEFAULT 0, response_bytes BIGINT NOT NULL DEFAULT 0,
 response_payload_id VARCHAR(64) NOT NULL DEFAULT '', safe_error_code VARCHAR(64) NOT NULL DEFAULT '',
 outcome VARCHAR(16) NOT NULL, finished_at DATETIME(3) NULL,
 KEY idx_video_request_org(org_id,started_at),
 KEY idx_video_request_unfinished(outcome,started_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
