-- G1-A expand only, following the merged 0128 response inbox migration.
-- Each CREATE / the combined ALTER is atomic in MySQL. Interrupted replay is
-- covered at every statement boundary. No existing permissions or money change.
CREATE TABLE routing_groups (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    slug VARCHAR(64) NOT NULL,
    name VARCHAR(128) NOT NULL,
    description VARCHAR(512) NOT NULL DEFAULT '',
    owner_org_id BIGINT UNSIGNED NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'draft',
    policy_version BIGINT UNSIGNED NOT NULL DEFAULT 1,
    created_at DATETIME(3) NOT NULL,
    updated_at DATETIME(3) NOT NULL,
    UNIQUE KEY uk_routing_group_slug (owner_org_id,slug),
    KEY idx_routing_group_owner (owner_org_id,status,id),
    CONSTRAINT ck_routing_group_status CHECK (status IN ('draft','enabled','disabled')),
    CONSTRAINT ck_routing_group_version CHECK (policy_version > 0),
    CONSTRAINT ck_routing_group_name CHECK (CHAR_LENGTH(TRIM(name)) > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_bin;

CREATE TABLE channel_group_bindings (
    group_id BIGINT UNSIGNED NOT NULL,
    channel_id BIGINT UNSIGNED NOT NULL,
    created_at DATETIME(3) NOT NULL,
    updated_at DATETIME(3) NOT NULL,
    PRIMARY KEY (group_id,channel_id),
    KEY idx_routing_channel_group (channel_id,group_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_bin;

CREATE TABLE org_group_grants (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    org_id BIGINT UNSIGNED NOT NULL,
    group_id BIGINT UNSIGNED NOT NULL,
    status VARCHAR(16) NOT NULL,
    grant_version BIGINT UNSIGNED NOT NULL DEFAULT 1,
    default_slot TINYINT UNSIGNED NULL DEFAULT NULL,
    created_at DATETIME(3) NOT NULL,
    updated_at DATETIME(3) NOT NULL,
    UNIQUE KEY uk_routing_org_group (org_id,group_id),
    UNIQUE KEY uk_routing_org_default (org_id,default_slot),
    KEY idx_routing_grant_org (org_id,status,group_id),
    KEY idx_routing_grant_group (group_id,org_id),
    CONSTRAINT ck_routing_grant_status CHECK (status IN ('enabled','revoked')),
    CONSTRAINT ck_routing_grant_version CHECK (grant_version > 0),
    CONSTRAINT ck_routing_grant_default CHECK (default_slot IS NULL OR (default_slot=1 AND status='enabled')),
    CONSTRAINT ck_routing_grant_ids CHECK (org_id > 0 AND group_id > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_bin;

CREATE TABLE routing_policy_events (
    id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    aggregate_type VARCHAR(16) NOT NULL,
    aggregate_id BIGINT UNSIGNED NOT NULL,
    aggregate_version BIGINT UNSIGNED NOT NULL,
    event_kind VARCHAR(32) NOT NULL,
    org_id BIGINT UNSIGNED NULL,
    group_id BIGINT UNSIGNED NULL,
    channel_id BIGINT UNSIGNED NULL,
    key_id BIGINT UNSIGNED NULL,
    actor_user_id BIGINT UNSIGNED NOT NULL,
    request_id VARCHAR(64) NOT NULL DEFAULT '',
    created_at DATETIME(3) NOT NULL,
    UNIQUE KEY uk_routing_policy_event (aggregate_type,aggregate_id,aggregate_version),
    CONSTRAINT ck_routing_event_aggregate CHECK (aggregate_type IN ('group','grant','key')),
    CONSTRAINT ck_routing_event_version CHECK (aggregate_id > 0 AND aggregate_version > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_bin;

ALTER TABLE api_keys
    ADD COLUMN routing_group_id BIGINT UNSIGNED NULL DEFAULT NULL,
    ADD COLUMN model_access_mode VARCHAR(32) CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_bin NOT NULL DEFAULT 'legacy',
    ADD COLUMN policy_version BIGINT UNSIGNED NOT NULL DEFAULT 1,
    ADD KEY idx_api_key_routing_group (routing_group_id,id),
    ADD CHECK ((model_access_mode='legacy' AND routing_group_id IS NULL) OR (model_access_mode IN ('inherit_group','subset') AND routing_group_id IS NOT NULL AND routing_group_id > 0)),
    ADD CHECK (policy_version > 0);
