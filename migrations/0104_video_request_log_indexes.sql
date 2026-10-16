-- Stable pagination for the organization's request log timeline.
ALTER TABLE api_request_logs
 ADD KEY idx_video_request_org_cursor (org_id, started_at, request_id);
