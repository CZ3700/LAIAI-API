-- 0029_org_invitations.sql
-- 团队邀请确认流程（用户要求）：加成员不再直接拉人入伙，先发 pending 邀请，
-- 被邀请人 accept 才真正成为成员。未经同意把人拉进团队 = 让其访问团队共享余额，
-- 属越权（红线：org_id 租户隔离）。一条「活动邀请」= 本表一行；accept/decline/revoke 后删除，
-- 审计留痕走 audit_logs。UNIQUE(org_id, invitee_user_id) 保证同一被邀请人对同一团队至多一条待处理邀请。
CREATE TABLE org_invitations (
    id              BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
    org_id          BIGINT UNSIGNED NOT NULL,
    inviter_user_id BIGINT UNSIGNED NOT NULL,
    invitee_user_id BIGINT UNSIGNED NOT NULL,
    role            VARCHAR(16) NOT NULL DEFAULT 'member' COMMENT 'admin|member|billing（owner 不经邀请流程）',
    created_at      BIGINT NOT NULL,
    PRIMARY KEY (id),
    UNIQUE KEY uk_invite_org_user (org_id, invitee_user_id),
    KEY idx_invite_invitee (invitee_user_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;
