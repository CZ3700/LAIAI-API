-- 0039_realtime_audio_pricing.sql
-- Realtime WS relay（商用迭代指令 §3.9）：音频 token 定价。
-- OpenAI Realtime 的音频 input/output token 比文本贵约 10-20×，不能用文本价近似。
-- realtime 会话 cost = text_in×input_price + text_out×output_price
--                    + audio_in×audio_input_price + audio_out×audio_output_price（全 ×groupRatio）。
-- 0 = 未配置该模型的音频价（realtime 模型须显式配；非 realtime 模型恒 0，不影响 chat/task 计价）。
ALTER TABLE model_prices
    ADD COLUMN audio_input_usd_per_1m  DECIMAL(20,8) NOT NULL DEFAULT 0,
    ADD COLUMN audio_output_usd_per_1m DECIMAL(20,8) NOT NULL DEFAULT 0;
