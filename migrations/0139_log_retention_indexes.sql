-- Retention sweep indexes (0139): nightly batch deletes by pure time range
-- need standalone time-column indexes; the existing composite indexes all
-- lead with org_id/task_id and cannot serve them. Index-only change,
-- CREATE INDEX is replayable after its implicit commit and before
-- schema_migrations records this version.
CREATE INDEX idx_api_request_logs_started ON api_request_logs (started_at);
CREATE INDEX idx_video_request_contexts_created ON video_request_contexts (created_at);
CREATE INDEX idx_video_events_created ON video_events (created_at);
CREATE INDEX idx_video_attempts_observed ON video_attempts (observed_at);
CREATE INDEX idx_video_outbox_created ON video_outbox (created_at);
CREATE INDEX idx_video_asset_events_created ON video_asset_events (created_at);
