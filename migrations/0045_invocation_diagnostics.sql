-- 0045_invocation_diagnostics.sql
-- 排障闭环（顶级审计修 2026-07 排名 5）：invocations 增列——请求详情字段真实化 + 历史日志可诊断。
--   finish_reason / protocol：此前前端硬编码伪造（协议恒显 OpenAI、finish_reason 恒 stop）。
--   cached_tokens / reasoning_tokens：计费内部已按缓存价/思考 token 处理，观测层此前丢弃拆分。
--   upstream_error_code：上游失败真因——仅存分类 reason 或上游错误体的白名单 code/type 枚举字段
--   （红线 7/8：绝不落自由文本 message，可能回显 prompt 碎片）。
-- 存量行全部默认空/0（历史数据不回填，与 0040 modality 同纪律）。
ALTER TABLE invocations
    ADD COLUMN finish_reason VARCHAR(32) NOT NULL DEFAULT '' COMMENT '真实 finish_reason（空=历史行/非 chat 模态）',
    ADD COLUMN protocol VARCHAR(16) NOT NULL DEFAULT '' COMMENT '入站方言 openai|anthropic|responses（空=历史行）',
    ADD COLUMN cached_tokens BIGINT NOT NULL DEFAULT 0 COMMENT '缓存命中输入 token 子集',
    ADD COLUMN reasoning_tokens BIGINT NOT NULL DEFAULT 0 COMMENT '思考 token 子集（completion 内）',
    ADD COLUMN upstream_error_code VARCHAR(64) NOT NULL DEFAULT '' COMMENT '上游错误码（白名单枚举，无自由文本）';
