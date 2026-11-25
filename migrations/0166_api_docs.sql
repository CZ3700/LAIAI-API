-- 0166_api_docs.sql
-- 接口文档中心目录树与正文表，单表独立原子 DDL
CREATE TABLE api_docs (
    id               BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    parent_id        BIGINT UNSIGNED DEFAULT NULL,
    title            VARCHAR(255)    NOT NULL,
    slug             VARCHAR(128)    NOT NULL DEFAULT '',
    content          LONGTEXT        NOT NULL,
    is_dir           TINYINT         NOT NULL DEFAULT 0,
    sort_order       INT             NOT NULL DEFAULT 0,
    is_active        TINYINT         NOT NULL DEFAULT 1,
    category_id      BIGINT UNSIGNED DEFAULT NULL,
    is_preset        TINYINT         NOT NULL DEFAULT 0,
    default_expanded TINYINT         NOT NULL DEFAULT 0,
    created_at       DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at       DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    KEY idx_api_docs_parent_sort (parent_id, sort_order, id),
    KEY idx_api_docs_slug (slug),
    KEY idx_api_docs_active (is_active, sort_order, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
