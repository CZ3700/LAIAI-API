-- Per-public-model admission limits, admin-settable (Volcano-style):
-- max_concurrent_tasks caps in-flight tasks per model (0 = fall back to the
-- channel-level gate only); create_rpm caps admissions per 60s window per
-- model platform-wide (0 = unlimited). Both are admission-side, never touch
-- worker dispatch. Single atomic ALTER is replayable after its implicit
-- commit and before schema_migrations records this version.
ALTER TABLE video_model_configs
 ADD COLUMN max_concurrent_tasks INT NOT NULL DEFAULT 0,
 ADD COLUMN create_rpm INT NOT NULL DEFAULT 0;
