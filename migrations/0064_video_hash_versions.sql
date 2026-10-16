CREATE TABLE video_hash_versions (
 version VARCHAR(32) PRIMARY KEY, fingerprint CHAR(64) NOT NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
