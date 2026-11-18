-- 真人认证 Action RPC 透传端点支撑（2026-11-18）。
-- byted_token_hash：客户 BytedToken 的 HMAC-SHA256 指纹（digest(ActiveHMACKey,
-- "byted-token",0,"",token)，不落明文），供 ?Action=GetVisualValidateResult 反查
-- 授权会话；key 轮换时按全部 HMACKeys 版本依次匹配。存量行为空仅新会话写入。
-- idempotency_key：同步创建端点的幂等定位（org+key+键唯一）。
ALTER TABLE video_portrait_authorizations ADD COLUMN byted_token_hash varchar(64) NOT NULL DEFAULT '';
ALTER TABLE video_portrait_authorizations ADD COLUMN idempotency_key varchar(140) NOT NULL DEFAULT '';
CREATE INDEX idx_video_portrait_byted_token_hash ON video_portrait_authorizations (byted_token_hash);
CREATE INDEX idx_video_portrait_idempotency ON video_portrait_authorizations (org_id, created_by_key_id, idempotency_key);
