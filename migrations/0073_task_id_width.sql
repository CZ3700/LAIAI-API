-- Existing legacy IDs are retained; official gateway IDs carry 128 random bits.
-- migrate:ack-destructive
-- Reviewed widening only: task_id 40->64, status 16->32 (submission_unknown),
-- price precision (12,6)->(24,12). No shrink, deletion, rename or data rewrite.
-- Before production: verified backup, check ALTER lock/space on a replica,
-- apply before enabling Ark workers; rollback application code, never narrow
-- these columns after new values exist. Tested on isolated MySQL 8.4 only.
ALTER TABLE tasks
 MODIFY COLUMN task_id VARCHAR(64) NOT NULL,
 MODIFY COLUMN status VARCHAR(32) NOT NULL,
 MODIFY COLUMN price_out_usd_per_1m DECIMAL(24,12) NOT NULL;
