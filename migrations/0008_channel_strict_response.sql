-- 0008_channel_strict_response.sql
-- 上游响应校验开关（部署冒烟暴露：上游 200 但返回 HTML 时被当成功透传+估算计费）。
-- strict_response=1（默认）：响应 content-type 非 JSON/SSE 时判为上游异常，不透传不计费。
-- 置 0 放宽（罕见：某些自定义上游故意返回非标准内容时）。

ALTER TABLE channels ADD COLUMN strict_response TINYINT NOT NULL DEFAULT 1;
