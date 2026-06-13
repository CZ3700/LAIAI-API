-- 0018: 灵活支付渠道（多实例、可启停、优先级、加密配置）。
-- 取代「扁平 ext_settings 里单套 stripe/epay 密钥」的局限：可配多条同协议渠道、
-- 按优先级路由、单独启停与连通性测试。密钥用信封加密（LAIAI_DATA_KEYS），
-- 与 relay 渠道凭证同一加密体系；明文永不落库、永不回显。
CREATE TABLE payment_channels (
    id            BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    code          VARCHAR(64)  NOT NULL,                       -- 唯一标识，进回调 URL /api/payment/callback/{code}
    display_name  VARCHAR(128) NOT NULL DEFAULT '',
    protocol      VARCHAR(32)  NOT NULL,                       -- stripe | epay
    config_enc    TEXT         NOT NULL,                       -- 信封加密的 JSON（密钥等敏感配置）
    enabled       TINYINT      NOT NULL DEFAULT 1,
    priority      INT          NOT NULL DEFAULT 0,             -- 同支付方式多渠道时，数小者优先
    pay_methods   VARCHAR(255) NOT NULL DEFAULT '',            -- CSV: card,alipay,wechat
    currency      VARCHAR(8)   NOT NULL DEFAULT 'USD',
    last_test_at  BIGINT       NOT NULL DEFAULT 0,
    last_test_ok  TINYINT      NOT NULL DEFAULT 0,
    last_test_msg VARCHAR(512) NOT NULL DEFAULT '',
    created_at    DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at    DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    PRIMARY KEY (id),
    UNIQUE KEY uk_payment_channels_code (code)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
