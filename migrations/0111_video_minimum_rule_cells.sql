-- Ark console pricing evidence and auditable fund activity.
CREATE TABLE video_minimum_rule_cells (
 id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT PRIMARY KEY,
 rule_set_id BIGINT UNSIGNED NOT NULL, resolution VARCHAR(16) NOT NULL,
 ratio VARCHAR(16) NOT NULL, output_duration_ms BIGINT NOT NULL,
 fps BIGINT NOT NULL, minimum_tokens BIGINT NOT NULL,
 UNIQUE KEY uk_video_minimum_cell (rule_set_id,resolution,ratio,output_duration_ms,fps)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
