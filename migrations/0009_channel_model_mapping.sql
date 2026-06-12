-- 0009_channel_model_mapping.sql
-- 渠道级模型映射：对外模型名 → 该渠道上游真实模型名（JSON object，空=不映射）。
-- 场景：中转站/Azure deployment 的内部名与平台对外名不一致；
-- 计费、调用记录、对外响应一律用对外名，仅上游请求体里换名。
-- DEFAULT ('')：MySQL 8.0.13+ 表达式默认（TEXT 不接受裸字面量默认）。

ALTER TABLE channels ADD COLUMN model_mapping TEXT NOT NULL DEFAULT ('');
