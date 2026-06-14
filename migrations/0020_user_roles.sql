-- 0020: 超管 RBAC —— 用户角色（从「共享 X-Admin-Token 裸奔」升级为可追责的多角色运营）。
-- 角色线性等级：user < support < admin < superadmin。
--   user       普通用户，无后台
--   support    只读后台 + 用户支持（封禁/加余额），不碰渠道/支付/全局配置
--   admin      绝大多数后台功能，不含角色管理与最危险的全局开关
--   superadmin 全部，含给他人授角色
-- X-Admin-Token 保留为「root 破玻璃」凭证（视同 superadmin），用于引导第一个超管。
ALTER TABLE users ADD COLUMN role VARCHAR(16) NOT NULL DEFAULT 'user';
CREATE INDEX idx_users_role ON users (role);
