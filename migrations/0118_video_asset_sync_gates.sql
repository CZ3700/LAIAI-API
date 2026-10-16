-- Serialize account-level dispatch eligibility without holding a DB lock during
-- provider I/O. The key hashes the non-secret account/region identity.
CREATE TABLE video_asset_sync_gates (
 account_key CHAR(64) CHARACTER SET ascii COLLATE ascii_bin PRIMARY KEY,
 version BIGINT UNSIGNED NOT NULL DEFAULT 0,
 next_dispatch_at DATETIME(3) NULL
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
