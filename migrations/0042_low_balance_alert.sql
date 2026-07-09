-- 0042_low_balance_alert.sql
-- organizations 加余额低预警（上线冲刺批次 3；差距清查 P1"用户级通知最小集"）。
-- low_balance_alert_usd：org 自设阈值，0=关闭（默认）；可用余额（posted-held）低于阈值时
-- 给 owner 发邮件。low_balance_alerted_at：上次发送时间，24h 内不重发（防轰炸）。
-- 扫描在每小时维护循环（leader-only），不进数据面热路径。
ALTER TABLE organizations
    ADD COLUMN low_balance_alert_usd DECIMAL(20,8) NOT NULL DEFAULT 0 COMMENT '余额低预警阈值 USD，0=关闭（0042）',
    ADD COLUMN low_balance_alerted_at DATETIME NULL COMMENT '上次预警发送时间（24h 防重发）';
