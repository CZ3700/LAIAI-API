-- 0019: 渠道级 HTTP 代理（对标 NewAPI 渠道代理设置）。
-- 上游调用可经指定代理出网——用于地区受限的上游 API（如部分海外厂商）。
-- 空 = 直连。格式：http://user:pass@host:port 或 socks5://host:port。
ALTER TABLE channels ADD COLUMN proxy VARCHAR(255) NOT NULL DEFAULT '';
