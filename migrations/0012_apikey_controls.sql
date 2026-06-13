-- 0012_apikey_controls.sql
-- API Key 精细管控（防 key 泄露烧光余额的安全刚需）：
--   quota_limit  每 key 累计消费上限 USD（0=无限）
--   used_amount  该 key 累计已消费 USD（capture 后原子累加）
--   model_limits 模型白名单，逗号分隔（空=不限）
--   allow_ips    调用方 IP 白名单，逗号/换行分隔，支持 CIDR（空=不限）

ALTER TABLE api_keys ADD COLUMN quota_limit  DECIMAL(20,8) NOT NULL DEFAULT 0;
ALTER TABLE api_keys ADD COLUMN used_amount  DECIMAL(20,8) NOT NULL DEFAULT 0;
ALTER TABLE api_keys ADD COLUMN model_limits TEXT NOT NULL DEFAULT ('');
ALTER TABLE api_keys ADD COLUMN allow_ips    TEXT NOT NULL DEFAULT ('');
