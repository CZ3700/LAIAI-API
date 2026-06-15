-- 0026_model_meta.sql
-- 模型元数据（gap P2-D，对标 NewAPI model/vendor 表）：厂商/分类/标签/描述/上下文长度/图标。
-- 纯展示元数据，不参与计费/路由；前端据此替代「按模型名 regex 猜厂商/分类」。
-- model 对应 model_prices.model（同一模型名空间）；缺元数据时前端回退到旧的猜测逻辑。
CREATE TABLE model_meta (
    model          VARCHAR(128) NOT NULL,
    vendor         VARCHAR(64)  NOT NULL DEFAULT '' COMMENT '厂商，如 OpenAI/Anthropic/DeepSeek',
    category       VARCHAR(32)  NOT NULL DEFAULT '' COMMENT '分类，如 chat/image/embedding/audio',
    tags           VARCHAR(255) NOT NULL DEFAULT '' COMMENT '逗号分隔标签，如 vision,tools,reasoning',
    description    VARCHAR(500) NOT NULL DEFAULT '',
    context_length INT          NOT NULL DEFAULT 0 COMMENT '上下文长度（token），0=未知',
    icon           VARCHAR(128) NOT NULL DEFAULT '' COMMENT '图标标识（@lobehub/icons 名或 URL）',
    updated_at     BIGINT       NOT NULL DEFAULT 0,
    PRIMARY KEY (model)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
