-- 0007_image_prices.sql
-- Catalog 多单位计费第一步（设计 §4.3：(model, unit, tier) 三元组的最小落地）：
-- 图像生成按张计价。size/quality 档位差价用"档位即模型"表达（如 dall-e-3-hd 单独定价行），
-- 避免过早引入 tier 维度表——量级与需求到了再泛化。

ALTER TABLE model_prices ADD COLUMN image_usd DECIMAL(12,6) NOT NULL DEFAULT 0 COMMENT '每张图 USD 价；0 = 非图像模型';
