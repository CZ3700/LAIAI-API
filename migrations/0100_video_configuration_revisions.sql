-- Immutable publication identity prevents reuse of a price/capability version
-- with changed semantics. Contains no credentials, prompts or raw evidence.
CREATE TABLE video_configuration_revisions (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
  public_model VARCHAR(64) NOT NULL,
  version VARCHAR(64) NOT NULL,
  capability_version VARCHAR(64) NOT NULL,
  capability_hash CHAR(64) NOT NULL,
  pricing_version VARCHAR(64) NOT NULL,
  provider_scope_id BIGINT UNSIGNED NOT NULL,
  channel_id BIGINT UNSIGNED NOT NULL,
  created_by BIGINT UNSIGNED NOT NULL,
  evidence_hash CHAR(64) NOT NULL,
  created_at DATETIME(3) NOT NULL,
  UNIQUE KEY uq_video_config_revision (public_model, version),
  KEY idx_video_capability_revision (public_model, capability_version)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
