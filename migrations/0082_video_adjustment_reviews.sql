-- Ark S2: durable video_adjustment_reviews
CREATE TABLE video_adjustment_reviews (
 id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
 proposal_id BIGINT UNSIGNED NOT NULL, org_id BIGINT UNSIGNED NOT NULL,
 reviewer_id BIGINT UNSIGNED NOT NULL, approved BOOLEAN NOT NULL,
 reason VARCHAR(64) NOT NULL, created_at DATETIME(3) NOT NULL,
 UNIQUE KEY uk_video_adjustment_review(proposal_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_bin;
