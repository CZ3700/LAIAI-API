-- 0016_affiliate.sql
-- 邀请返佣：每个组织有邀请码(aff_code)，被邀请的组织记 inviter_org_id。
-- 充值返佣模式（比注册赠送防薅）：被邀请组织充值入账后，按 affiliate_rate（settings 热更新）
-- 给邀请组织返佣入账。返佣 idempotency=affiliate:<order_no> 防重复。

-- aff_code 默认 NULL（NULL 可重复，UNIQUE 只约束非空值）；新注册组织生成非空唯一码。
ALTER TABLE organizations ADD COLUMN aff_code       VARCHAR(16) DEFAULT NULL;
ALTER TABLE organizations ADD COLUMN inviter_org_id BIGINT NOT NULL DEFAULT 0;

CREATE UNIQUE INDEX uk_org_aff_code ON organizations (aff_code);
