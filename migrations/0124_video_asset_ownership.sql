-- Creator isolation for private Ark resources. Unknown historical ownership is
-- deliberately retained as zero/zero (named superadministrators only).
CREATE TABLE video_asset_owners (
 org_id BIGINT UNSIGNED NOT NULL,
 resource_type VARCHAR(32) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 resource_id VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 owner_user_id BIGINT UNSIGNED NOT NULL DEFAULT 0,
 owner_key_id BIGINT UNSIGNED NOT NULL DEFAULT 0,
 manage_all BOOLEAN NOT NULL DEFAULT FALSE,
 PRIMARY KEY(org_id,resource_type,resource_id),
 KEY idx_video_asset_owner_list(org_id,owner_user_id,owner_key_id,resource_type,resource_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;

-- Authenticated request metadata is the only historical creator authority.
-- Exact operation/resource/scope identity must match; names never participate.
INSERT INTO video_asset_owners(org_id,resource_type,resource_id,owner_user_id,owner_key_id)
SELECT g.org_id,'asset_group',g.group_id,COALESCE(r.user_id,0),
 CASE WHEN COALESCE(r.user_id,0)>0 THEN 0 ELSE COALESCE(r.api_key_id,0) END
FROM video_asset_groups g
LEFT JOIN video_asset_operations o ON o.org_id=g.org_id AND o.operation_id=g.create_operation_id
 AND o.resource_type='asset_group' AND o.resource_id=g.group_id AND o.action='CreateAssetGroup'
 AND o.scope_id=g.scope_id AND o.scope_version=g.scope_version AND o.account_id=g.account_id AND o.region=g.region AND o.project=g.project
LEFT JOIN api_request_logs r ON r.org_id=g.org_id AND r.request_id=o.request_id AND r.auth_outcome='authenticated';

INSERT INTO video_asset_owners(org_id,resource_type,resource_id,owner_user_id,owner_key_id)
SELECT org_id,'portrait',session_id,owner_user_id,CASE WHEN owner_user_id>0 THEN 0 ELSE created_by_key_id END
FROM video_portrait_authorizations;

UPDATE video_asset_owners own
JOIN video_asset_groups g ON own.org_id=g.org_id AND own.resource_type='asset_group' AND own.resource_id=g.group_id
JOIN video_portrait_authorizations p ON p.org_id=g.org_id AND p.session_id=g.portrait_id AND p.group_id=g.group_id
SET own.owner_user_id=p.owner_user_id,own.owner_key_id=CASE WHEN p.owner_user_id>0 THEN 0 ELSE p.created_by_key_id END
WHERE g.kind='real' AND own.owner_user_id=0 AND own.owner_key_id=0;

INSERT INTO video_asset_owners(org_id,resource_type,resource_id,owner_user_id,owner_key_id)
SELECT a.org_id,'asset',a.asset_id,COALESCE(r.user_id,0),
 CASE WHEN COALESCE(r.user_id,0)>0 THEN 0 ELSE COALESCE(r.api_key_id,0) END
FROM video_assets a
LEFT JOIN video_asset_groups g ON g.org_id=a.org_id AND g.group_id=a.group_id
LEFT JOIN video_asset_operations o ON o.org_id=a.org_id AND o.operation_id=a.create_operation_id
 AND o.resource_type='asset' AND o.resource_id=a.asset_id AND o.action='CreateAsset'
 AND o.scope_id=a.scope_id AND o.scope_version=a.scope_version AND o.account_id=g.account_id AND o.region=g.region AND o.project=g.project
LEFT JOIN api_request_logs r ON r.org_id=a.org_id AND r.request_id=o.request_id AND r.auth_outcome='authenticated';

-- Official imports with no create operation inherit the already proven group
-- creator; an unattributed group remains administrator-only.
UPDATE video_asset_owners own
JOIN video_assets a ON own.org_id=a.org_id AND own.resource_type='asset' AND own.resource_id=a.asset_id
JOIN video_asset_details d ON d.org_id=a.org_id AND d.resource_id=a.asset_id AND d.source='imported'
JOIN video_asset_owners parent ON parent.org_id=a.org_id AND parent.resource_type='asset_group' AND parent.resource_id=a.group_id
SET own.owner_user_id=parent.owner_user_id,own.owner_key_id=parent.owner_key_id
WHERE a.create_operation_id='' AND own.owner_user_id=0 AND own.owner_key_id=0;

-- An operation is visible to the resource owner, including background queries
-- and administrator operations performed on that owner's resource.
INSERT INTO video_asset_owners(org_id,resource_type,resource_id,owner_user_id,owner_key_id)
SELECT o.org_id,'operation',o.operation_id,COALESCE(parent.owner_user_id,0),COALESCE(parent.owner_key_id,0)
FROM video_asset_operations o
LEFT JOIN video_asset_owners parent ON parent.org_id=o.org_id AND parent.resource_type=o.resource_type AND parent.resource_id=o.resource_id;

INSERT INTO video_asset_owners(org_id,resource_type,resource_id,owner_user_id,owner_key_id)
SELECT org_id,'upload',id,user_id,CASE WHEN user_id>0 THEN 0 ELSE key_id END
FROM video_media_uploads;

INSERT INTO video_asset_owners(org_id,resource_type,resource_id,owner_user_id,owner_key_id)
SELECT j.org_id,'sync',j.id,COALESCE(r.user_id,0),
 CASE WHEN COALESCE(r.user_id,0)>0 THEN 0 ELSE COALESCE(r.api_key_id,0) END
FROM video_asset_sync_jobs j
LEFT JOIN api_request_logs r ON r.org_id=j.org_id AND r.request_id=j.request_id AND r.auth_outcome='authenticated';
