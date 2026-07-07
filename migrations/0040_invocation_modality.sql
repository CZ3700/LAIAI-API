-- 0040_invocation_modality.sql
-- invocations 加 modality 模态标（chat|embeddings|rerank|image|audio_speech|audio_transcription|realtime|video|music）。
-- 用途：请求日志按模态过滤（图像日志页 /app/image-logs 等）。存量行 modality=''（历史数据不回填，
-- 模态过滤只对新数据生效——回填需按 model 反推，不可靠，宁缺勿错）。
ALTER TABLE invocations
    ADD COLUMN modality VARCHAR(24) NOT NULL DEFAULT '' COMMENT '调用模态（空=0040 前的历史行）',
    ADD KEY idx_inv_org_modality (org_id, modality, id);
