-- 0048: 任务计费快照补 PerCallUSD（丝滑度批 C-4 Midjourney 前置资金修复）
-- settleSuccess 从快照重建价格时此前只有 output_usd_per_1m——按次计价（PerCallUSD）模型
-- （MJ 各 action）成功结算会算出 0（成功却收 0）。快照纪律：提交时定价，结算不读实时价。
ALTER TABLE tasks ADD COLUMN price_per_call_usd DECIMAL(20,8) NOT NULL DEFAULT 0;
