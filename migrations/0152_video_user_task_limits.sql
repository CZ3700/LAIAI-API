-- Per-user Seedance task limits. 0 means no local limit; revoking the group
-- grant remains the way to deny all new work. Monetary budgets continue to
-- use video_quota_policies and video_quota_buckets (scope='user').
CREATE TABLE video_user_task_limits (
 org_id BIGINT UNSIGNED NOT NULL,
 user_id BIGINT UNSIGNED NOT NULL,
 create_task_rpm INT UNSIGNED NOT NULL DEFAULT 0,
 max_inflight_tasks INT UNSIGNED NOT NULL DEFAULT 0,
 daily_task_limit BIGINT UNSIGNED NOT NULL DEFAULT 0,
 monthly_task_limit BIGINT UNSIGNED NOT NULL DEFAULT 0,
 timezone VARCHAR(64) NOT NULL DEFAULT 'Asia/Shanghai',
 version BIGINT UNSIGNED NOT NULL DEFAULT 1,
 updated_by BIGINT UNSIGNED NOT NULL,
 updated_at DATETIME(3) NOT NULL,
 PRIMARY KEY (org_id,user_id),
 CONSTRAINT ck_video_user_task_limit_ids CHECK (org_id > 0 AND user_id > 0),
 CONSTRAINT ck_video_user_task_limit_version CHECK (version > 0)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_bin;

-- A paced per-user outbound send gate prevents accepted backlog from
-- violating the user's task RPM in a later minute. Provider/account gates
-- still apply independently to every actual outbound create attempt.
CREATE TABLE video_user_send_gates (
 org_id BIGINT UNSIGNED NOT NULL,
 user_id BIGINT UNSIGNED NOT NULL,
 next_allowed_at DATETIME(3) NOT NULL,
 updated_at DATETIME(3) NOT NULL,
 PRIMARY KEY (org_id,user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_bin;

-- Worker candidate selection must skip an RPM-deferred user's operation
-- before scope ranking; otherwise the oldest deferred task starves other
-- users sharing that provider scope.
ALTER TABLE video_operations
 ADD COLUMN next_dispatch_at DATETIME(3) NULL,
 ADD KEY idx_video_operation_user_defer (status,next_dispatch_at,created_at,id);

-- Both counts remain current reads in the admission transaction. Task rows
-- are the durable source, including failed and no-charge accepted tasks.
ALTER TABLE tasks
 ADD KEY idx_video_user_admission (org_id,user_id,provider,submitted_at,id),
 ADD KEY idx_video_user_inflight (org_id,user_id,provider,status,id);

-- Account-wide concurrency checks start from this bounded prefix, then join
-- task status by org/task ID. It avoids scanning every historical task.
ALTER TABLE video_task_parameters
 ADD KEY idx_video_task_params_account_model (account_ref,public_model,org_id,task_id);

-- Official CreateTask limits are shared by the main account and foundation
-- model, even when that account has multiple provider scopes/projects. A
-- failed sync never changes the last trusted snapshot. Manual values are
-- independent safety ceilings and do not allocate the official pool to users.
CREATE TABLE video_provider_model_quotas (
 account_ref VARCHAR(64) NOT NULL,
 foundation_model VARCHAR(128) NOT NULL,
 official_create_task_rpm INT UNSIGNED NULL,
 official_concurrent_requests INT UNSIGNED NULL,
 official_create_task_rpm_4k INT UNSIGNED NULL,
 official_concurrent_requests_4k INT UNSIGNED NULL,
 manual_create_task_rpm INT UNSIGNED NULL,
 manual_concurrent_requests INT UNSIGNED NULL,
 manual_create_task_rpm_4k INT UNSIGNED NULL,
 manual_concurrent_requests_4k INT UNSIGNED NULL,
 official_observed_at DATETIME(3) NULL,
 manual_updated_at DATETIME(3) NULL,
 manual_updated_by BIGINT UNSIGNED NULL,
 manual_version BIGINT UNSIGNED NOT NULL DEFAULT 0,
 official_request_id VARCHAR(128) NOT NULL DEFAULT '',
 PRIMARY KEY (account_ref,foundation_model),
 CONSTRAINT ck_video_provider_quota_official_pair CHECK ((official_create_task_rpm IS NULL) = (official_concurrent_requests IS NULL))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_bin;

-- One outbound pacing row per main account and foundation model. 4K spends
-- both the general slot and its separate 4K slot; channel/scope count never
-- multiplies these official limits.
CREATE TABLE video_provider_model_send_gates (
 account_ref VARCHAR(64) NOT NULL,
 foundation_model VARCHAR(128) NOT NULL,
 next_allowed_at DATETIME(3) NOT NULL,
 next_allowed_at_4k DATETIME(3) NULL,
 updated_at DATETIME(3) NOT NULL,
 PRIMARY KEY (account_ref,foundation_model)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_bin;
