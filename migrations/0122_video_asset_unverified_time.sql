-- migrate:ack-destructive Relax only nullability; preserve every existing timestamp.
-- See production runbook section 6.3: encrypted backup, strict-MySQL replay,
-- row-count/timestamp preservation, and application-only rollback.
-- Imported official assets have not passed local media verification yet.
ALTER TABLE video_assets MODIFY COLUMN verified_at DATETIME(3) NULL;
