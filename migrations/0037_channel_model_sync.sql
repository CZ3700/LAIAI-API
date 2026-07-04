-- 0037_channel_model_sync.sql
-- 上游模型自动发现闭环（商用迭代指令 §3.10；设计 §4.4）：
--   - model_sync_enabled：opt-in 周期自动检测（默认关；后台 leader job 只扫启用渠道）。
--   - last_sync_at：上次自动检测时刻。
--   - detected_models：上次检测出的"上游新增、渠道未配、未忽略"模型（CSV），admin 端展示徽标。
--   - ignored_models：忽略清单（CSV）——detect 不再反复提示这些模型（上游有但运营不打算上货）。
-- apply/mark-removed 只改 channels.models，绝不自动写 model_prices（定价仍手动，验收"不自动公开未定价"）。
-- DEFAULT ('')：MySQL 8.0.13+ 表达式默认（TEXT 不接受裸字面量默认，0009 同款）。
ALTER TABLE channels
    ADD COLUMN model_sync_enabled TINYINT NOT NULL DEFAULT 0,
    ADD COLUMN last_sync_at DATETIME(3) NULL,
    ADD COLUMN detected_models TEXT NOT NULL DEFAULT (''),
    ADD COLUMN ignored_models TEXT NOT NULL DEFAULT ('');
