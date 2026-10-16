CREATE TABLE video_task_parameters (
 task_id VARCHAR(64) NOT NULL PRIMARY KEY,
 org_id BIGINT UNSIGNED NOT NULL,
 schema_version BIGINT UNSIGNED NOT NULL,
 provider_scope_id BIGINT UNSIGNED NOT NULL,
 provider_scope_version BIGINT UNSIGNED NOT NULL,
 account_ref VARCHAR(64) NOT NULL,
 region VARCHAR(32) NOT NULL,
 project_name VARCHAR(64) NOT NULL,
 public_model VARCHAR(64) NOT NULL,
 upstream_model VARCHAR(128) NOT NULL,
 capability_version VARCHAR(128) NOT NULL,
 parameters_json TEXT NOT NULL,
 has_input_video BOOLEAN NOT NULL,
 input_duration_ms BIGINT NOT NULL,
 created_at DATETIME(3) NOT NULL,
 KEY idx_video_task_parameters_org(org_id,task_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
