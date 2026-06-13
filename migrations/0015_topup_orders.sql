-- 0015_topup_orders.sql
-- 用户自助充值订单。入金事实源仍是复式账本（ledger journal），订单只是支付侧状态机。
-- status: 0=pending / 1=paid / 2=failed / 4=expired（与旧平台同枚举，便于运维直觉）。
-- 入金红线（移植自旧平台血泪）：callback 接受 status IN (0,4)；终态(failed)不被晚到 callback 翻转；
--   provider 必须匹配（防跨网关回调攻击）；金额校验；入金 ledger idempotency=topup:<order_no> 防重复。

CREATE TABLE topup_orders (
    id           BIGINT AUTO_INCREMENT PRIMARY KEY,
    order_no     VARCHAR(64) NOT NULL UNIQUE,
    org_id       BIGINT NOT NULL,
    user_id      BIGINT,
    provider     VARCHAR(32) NOT NULL,          -- stripe / epay
    amount_usd   DECIMAL(20,8) NOT NULL,         -- 入账到余额的 USD
    pay_amount   DECIMAL(20,8) NOT NULL,         -- 实付金额（pay_currency 单位）
    pay_currency VARCHAR(8) NOT NULL DEFAULT 'USD',
    status       TINYINT NOT NULL DEFAULT 0,
    trade_no     VARCHAR(128) NOT NULL DEFAULT '', -- 上游交易号
    created_at   BIGINT NOT NULL,
    paid_at      BIGINT NOT NULL DEFAULT 0,
    INDEX idx_topup_org (org_id),
    INDEX idx_topup_status (status)
);
