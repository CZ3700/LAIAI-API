-- 0022_topup_provider_ref.sql
-- 对账地基（M4，红线 #5「每个支付引擎必须可主动 Query 对账」）：
-- 持久化网关侧引用（Stripe Checkout Session id），供 reconcile job 主动 QueryOrder。
-- epay 用 out_trade_no(=order_no) 即可查，无需此列；但 Stripe 必须按 session id 查会话，
-- 而 session id 只在 CreatePayment 返回时可得，不落库则对账无从查起。
ALTER TABLE topup_orders ADD COLUMN provider_ref VARCHAR(128) NOT NULL DEFAULT '';
