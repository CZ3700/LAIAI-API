-- 0167_api_docs_intl.sql
-- 接口文档多语言翻译表，单表独立原子 DDL
CREATE TABLE api_docs_intl (
    id         BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
    doc_id     BIGINT UNSIGNED NOT NULL,
    lang       VARCHAR(16)     NOT NULL,
    title      VARCHAR(255)    NOT NULL,
    content    LONGTEXT        NOT NULL,
    created_at DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    updated_at DATETIME(3)     NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
    UNIQUE KEY uk_api_docs_intl_doc_lang (doc_id, lang),
    KEY idx_api_docs_intl_lang (lang)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
