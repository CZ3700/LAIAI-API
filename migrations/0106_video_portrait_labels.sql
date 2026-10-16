-- Ark console private asset metadata.
CREATE TABLE video_portrait_labels (
 session_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
 org_id BIGINT UNSIGNED NOT NULL, name VARCHAR(256) NOT NULL DEFAULT '', description VARCHAR(1200) NOT NULL DEFAULT '',
 name_synced BOOLEAN NOT NULL DEFAULT FALSE, KEY idx_video_portrait_label_org(org_id,session_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
