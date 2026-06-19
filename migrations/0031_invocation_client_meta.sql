-- 0031: 调用记录补充访客侧元数据（请求日志详情 CLIENT 栏）。
-- client_ip = 经 Caddy X-Forwarded-For 解析的真实访客 IP（gin 默认信任代理，与 keyGate IP 白名单同源）；
-- user_agent / referer 截断存储。均属请求元数据（非 payload/PII 正文），随 30 天滚动留存一并清理。空=未采集。
-- 单条 ALTER 多列（纪律：多列合并，MySQL 一次 DDL）。
ALTER TABLE invocations ADD COLUMN client_ip VARCHAR(64) NOT NULL DEFAULT '', ADD COLUMN user_agent VARCHAR(256) NOT NULL DEFAULT '', ADD COLUMN referer VARCHAR(256) NOT NULL DEFAULT '';
