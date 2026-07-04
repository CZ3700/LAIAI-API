-- 0036_advanced_custom.sql
-- Advanced Custom Channel（商用迭代指令 §3.3；设计 §4.4）：运营不写代码接入"奇怪上游"。
--   - converter：格式转换器（none|openai_chat_to_anthropic_messages|
--     openai_chat_to_gemini_generate_content|openai_chat_to_openai_responses），空=按 provider。
--   - upstream_path：自定义端点路径，覆盖 adapter 默认 /chat/completions（空=默认）。
-- 自定义认证复用 header_override 的 {{credential}} 占位符（0033），无需新列。
-- SSRF 防护在代码层（配置期校验 + 拨号期 DNS-rebinding 复检），仅作用于 advanced_custom 渠道。
ALTER TABLE channels
    ADD COLUMN converter VARCHAR(48) NOT NULL DEFAULT '',
    ADD COLUMN upstream_path VARCHAR(255) NOT NULL DEFAULT '';
