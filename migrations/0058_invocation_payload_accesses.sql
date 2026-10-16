-- Ark video: durable invocation_payload_accesses
CREATE TABLE invocation_payload_accesses (
 access_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 phase VARCHAR(16) NOT NULL, org_id BIGINT UNSIGNED NOT NULL,
 payload_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 actor_role VARCHAR(32) NOT NULL, actor_id VARCHAR(64) NOT NULL,
 reason_code VARCHAR(64) NOT NULL, operation_id VARCHAR(64) NOT NULL DEFAULT '',
 outcome VARCHAR(16) NOT NULL, safe_error_code VARCHAR(64) NOT NULL DEFAULT '',
 created_at DATETIME(3) NOT NULL,
 PRIMARY KEY(access_id,phase),
 KEY idx_video_payload_access_org(org_id,payload_id,created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
