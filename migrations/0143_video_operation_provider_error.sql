-- legacy 派发路径（LAIAI_VIDEO_RESPONSE_INBOX=false，线上当前默认）没有收据行，
-- 上游拒绝信封改由操作行自身承载：persistResponse 在同一事务写入，reducer 读取后
-- 写 tasks.fail_reason；崩溃后恢复路径也能从操作行取回原因。
ALTER TABLE video_operations
 ADD COLUMN provider_error_code varchar(64) NOT NULL DEFAULT '' AFTER error_code,
 ADD COLUMN provider_error_message varchar(255) NOT NULL DEFAULT '' AFTER provider_error_code;
