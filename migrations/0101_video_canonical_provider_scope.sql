-- A billing account/region/project owns one stable quota domain. Before rollout
-- inspect duplicates; do not merge historical scope/billing rows automatically.
-- This migration intentionally fails on ambiguous existing scope identities.
ALTER TABLE video_provider_scopes
 ADD UNIQUE KEY uq_video_canonical_provider_scope (account_ref, region, project_name);
