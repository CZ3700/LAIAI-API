-- 0047_channel_disable_suggestion.sql
-- 渠道禁用建议（丝滑度批 B-2）：检测到硬故障（余额耗尽/token 失效/组织禁用等）时**不自动禁用**，
-- 而是记录一条禁用建议，admin 渠道列表显示、运营手动决定（符合"不随意封禁、丝滑优先"产品原则）。
-- 渠道一次成功请求即清除建议（撤销）。与既有 channel_auto_disable 开关（默认关）互补——
-- 开关关时只记建议不自动禁；开关开时（不推荐）才自动禁用。
ALTER TABLE channels
    ADD COLUMN suggest_disable_reason VARCHAR(255) NOT NULL DEFAULT '' COMMENT '禁用建议原因（硬故障分类，空=无建议）',
    ADD COLUMN suggest_disabled_at DATETIME NULL COMMENT '建议产生时间（成功请求即清）';
