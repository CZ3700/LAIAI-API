-- 0038_task_result_ref.sql
-- §3.8 批次 4（sora/gemini-veo）：产物经平台代理下发的厂商，产物内部引用落库。
--   - result_ref：上游产物引用（sora=上游 video id；veo=带鉴权的 files URI）。
--     仅供平台 content 代理端点（GET /v1/video/generations/{id}/content）用渠道凭证取流，
--     **绝不直接暴露给用户**（veo URI 带渠道 key，直接吐出=泄露凭证）；对外 result_url
--     指向平台代理路径。JSON 序列化标 json:"-"。
ALTER TABLE tasks
    ADD COLUMN result_ref VARCHAR(1024) NOT NULL DEFAULT '' COMMENT '产物内部引用（平台代理取流用，绝不外泄）';
