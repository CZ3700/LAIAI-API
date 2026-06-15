-- 0027_advanced_pricing.sql
-- 高级计费（gap P2-F）：按次固定价 + 音频计价 + 二层分组倍率。
--   per_call_usd               >0 时 chat/embeddings 按次固定收费（忽略 token），对标 NewAPI ModelPrice(按次)
--   tts_usd_per_1k_chars       TTS(/v1/audio/speech) 按输入字符计价，USD/千字符
--   transcription_usd_per_call 转录(/v1/audio/transcriptions) 每次固定价（时长不可靠，按次最稳妥）
-- 三列均默认 0（不影响既有 token 计价）。资金语义与 image_usd 同构（hold/capture 走 ledger）。
ALTER TABLE model_prices ADD COLUMN per_call_usd               DECIMAL(12,6) NOT NULL DEFAULT 0;
ALTER TABLE model_prices ADD COLUMN tts_usd_per_1k_chars       DECIMAL(12,6) NOT NULL DEFAULT 0;
ALTER TABLE model_prices ADD COLUMN transcription_usd_per_call DECIMAL(12,6) NOT NULL DEFAULT 0;

-- 二层分组倍率（gap P2-F）：在「组织分组倍率(group_ratios)」之上，再叠加「(分组,模型)」维度。
-- 最终计费倍率 = group_ratios.ratio × model_group_ratios.ratio（缺省各层均回退 1）。
-- 用途：某分组对特定模型单独打折/加价（如 VIP 组的 gpt-5 半价），无需改全局模型价。
CREATE TABLE model_group_ratios (
    group_name VARCHAR(64)   NOT NULL,
    model      VARCHAR(128)  NOT NULL,
    ratio      DECIMAL(12,6) NOT NULL DEFAULT 1,
    PRIMARY KEY (group_name, model)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
