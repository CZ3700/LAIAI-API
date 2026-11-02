-- Console request-log joins (ark_request_logs): the task detail/list query
-- joins video_task_states by create_request_id and EXISTS-checks
-- video_asset_operations by (org_id, operation_id) per log row. Both lacked
-- indexes, so every execution scanned the whole state table (and the 46k-row
-- asset table) per log row once api_request_logs grew past ~100k rows,
-- stacking multi-minute queries that pegged MySQL CPU. Index-only change,
-- CREATE INDEX is replayable after its implicit commit and before
-- schema_migrations records this version.
CREATE INDEX idx_video_state_origin ON video_task_states (org_id, create_request_id);
CREATE INDEX idx_video_asset_op_lookup ON video_asset_operations (org_id, operation_id);
