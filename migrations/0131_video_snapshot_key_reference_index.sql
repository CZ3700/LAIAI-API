-- Preserve historical Key billing identities without scanning an Org's history.
ALTER TABLE video_billing_snapshots
 ADD KEY idx_video_snapshot_key (org_id, api_key_id, id);
