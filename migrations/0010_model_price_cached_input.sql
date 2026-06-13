-- 0010_model_price_cached_input.sql
-- 缓存命中输入价：OpenAI/Anthropic/GLM 等普遍返回 cached_tokens（命中提示缓存的输入），
-- 上游对其收费打折（典型 0.1×）。此前我们按 input 全价计 = 对用户多收。
-- 新列存缓存输入单价；0 = 回退 input 全价（向后兼容，旧定价行不变价）。
-- DEFAULT 0：MySQL DECIMAL 接受数值字面量默认。

ALTER TABLE model_prices ADD COLUMN cached_input_usd_per_1m DECIMAL(20,8) NOT NULL DEFAULT 0;
