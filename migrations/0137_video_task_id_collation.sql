-- Align tasks.task_id (and video_request_contexts.task_id charset) to the
-- utf8mb4_bin identity collation used by the video family. Cross-collation
-- equality against utf8mb4_bin columns (video_task_states, video_operations,
-- api_request_logs) silently disabled ref access on uk_tasks_task_id: the
-- worker polling loop fell back to idx_tasks_org full-org scans per state row
-- (~2770x2769 comparisons, 16s per cycle) and pegged MySQL at 100% CPU once
-- daily task volume crossed ~1k. Collation-only change (same charset for
-- tasks), values are ASCII 'task_'+hex so ordering/semantics are unchanged.
-- Single ALTER per table is replayable after its implicit commit and before
-- schema_migrations records this version.
ALTER TABLE tasks MODIFY task_id varchar(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL;
ALTER TABLE video_request_contexts MODIFY task_id varchar(64) CHARACTER SET utf8mb4 COLLATE utf8mb4_bin NOT NULL;
