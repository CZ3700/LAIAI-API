-- 上游确定性拒绝的证据落点：加密 capture 常被关闭（capture_history=false），
-- 收据行是 sender → worker 唯一的结构化载体。provider_error_code 只接受
-- 文档形态的安全 token（响应解析侧已按 ^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$ 收紧），
-- provider_error_message 是租户隔离的自由文本（可能回显 prompt 片段），
-- 只用于 tasks.fail_reason / 任务查询回包，绝不进日志。
ALTER TABLE video_response_inbox
 ADD COLUMN provider_error_code varchar(64) NOT NULL DEFAULT '' AFTER safe_error_code,
 ADD COLUMN provider_error_message varchar(255) NOT NULL DEFAULT '' AFTER provider_error_code;
