-- 派发/轮询闸间隔从硬编码 1 秒改为按 (scope, action) 可配置，为 260 任务/分钟
-- 量级做准备：官方账户配额（2.0=50并发/600RPM，2.5=150/600）下，create 需要
-- ~5-10/s、query 需要 ~20/s 的闸容量。默认仍 1000ms，未覆盖的行为不变。
ALTER TABLE video_provider_rates
 ADD COLUMN interval_ms INT NOT NULL DEFAULT 1000 AFTER action;

UPDATE video_provider_rates SET interval_ms = 100 WHERE action = 'create';
UPDATE video_provider_rates SET interval_ms = 50 WHERE action = 'query';
