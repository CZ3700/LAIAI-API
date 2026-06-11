-- 0004_channel_priority.sql
-- 渠道优先级分层（知识库 §3：retry 序号映射优先级层，层内加权随机）。
-- priority 大者先用；同层按 weight 加权随机；零权重 = 同层均分（不是"别用我"）。

ALTER TABLE channels ADD COLUMN priority INT NOT NULL DEFAULT 0;
CREATE INDEX idx_channels_pick ON channels (status, priority, weight);
