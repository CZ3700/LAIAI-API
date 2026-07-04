-- 0033_channel_overrides.sql
-- 渠道级 Header/Param Override（商用迭代指令 §3.4；设计 §4.4）：运营不写代码接入"有方言的上游"。
--   - param_override：JSON object，进 adapter 前对请求体深合并；值 null = 删除该字段（嵌套亦然）。
--     受保护键（model/stream/max_tokens/max_completion_tokens/n/thinking/messages）写入时拒绝——
--     否则 override 抬高输出上限会绕过 hold 估算钳制（负余额风险）。
--   - header_override：JSON object，adapter 默认头之后应用（运营优先级最高）；值 null = 删除头；
--     {{credential}} 占位符注入渠道解密凭证（自定义认证头不落明文 key，红线 7）。Host/Content-Length 禁改。
-- DEFAULT ('')：MySQL 8.0.13+ 表达式默认（TEXT 不接受裸字面量默认，0009 同款）。

ALTER TABLE channels
    ADD COLUMN param_override TEXT NOT NULL DEFAULT (''),
    ADD COLUMN header_override TEXT NOT NULL DEFAULT ('');
