-- 0041_apikey_rpm.sql
-- api_keys 加 rpm_limit：key 级每分钟请求上限（上线冲刺批次 3；差距清查 P1"per-key 限流"）。
-- 0=不限（默认，向后兼容）。执行点在数据面 gateKey（keyGate 精细管控三项之后），
-- 计数窗口与 (org,model) 模型级限流共用同一 rateCounter（Redis 跨实例 / 进程内退化，fail-open）。
ALTER TABLE api_keys
    ADD COLUMN rpm_limit INT NOT NULL DEFAULT 0 COMMENT 'key 级每分钟请求上限，0=不限（0041）';
