-- 0021_org_entitlements.sql
-- Org Entitlements（P0-2 功能调配）：超级管理员按组织分配「可用功能」与「资源上限」。
-- 商业化多租户的能力面：每行 = 对某 org 的显式收紧；无行 = 平台默认（全功能开启、不限量），
-- 故既有 org 零影响，向后兼容。默认值在 Go 层 DefaultEntitlements 定义并与本表 DEFAULT 一致。
-- 强制点（原子、无 TOCTOU）：
--   feature_images → relay /v1/images/generations
--   feature_video  → task   /v1/video/generations
--   max_members    → org 加成员（同事务内 COUNT 比对）
--   max_api_keys   → 签发 API Key（同事务内 COUNT 比对，仅计 active）
-- 注：月度消费上限不在本期落地——invocations 30 天滚动清理，非可靠口径；
--    金额红线下「可设却不强制」比不设更危险，待 Phase 2 用独立聚合源精确实现。

CREATE TABLE org_entitlements (
    org_id         BIGINT UNSIGNED NOT NULL,
    feature_images TINYINT NOT NULL DEFAULT 1 COMMENT '1=允许图像生成',
    feature_video  TINYINT NOT NULL DEFAULT 1 COMMENT '1=允许视频生成',
    max_members    INT     NOT NULL DEFAULT 0 COMMENT 'org 成员上限，0=不限',
    max_api_keys   INT     NOT NULL DEFAULT 0 COMMENT '活跃 API Key 上限，0=不限',
    updated_at     DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    PRIMARY KEY (org_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
