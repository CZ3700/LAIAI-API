-- Ark 状态回调 hint 表（2026-11-06 起量准备）：回调只是“通知”，不是事实源。
-- hint 行仅用于触发权威查询与审计留档；重复推送按 (task_id, body_hash) 唯一键
-- 折叠。body_excerpt 只保留前 4KB 供排查，完整事实来自权威查询的既有证据链。
CREATE TABLE video_callback_hints (
 id BIGINT UNSIGNED PRIMARY KEY AUTO_INCREMENT,
 task_id VARCHAR(64) NOT NULL,
 body_hash CHAR(64) NOT NULL,
 body_excerpt TEXT NOT NULL,
 received_at DATETIME(3) NOT NULL,
 processed_at DATETIME(3) NULL,
 result VARCHAR(64) NOT NULL DEFAULT '',
 UNIQUE KEY uk_video_callback_hint (task_id, body_hash),
 KEY idx_video_callback_hint_due (processed_at, id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
