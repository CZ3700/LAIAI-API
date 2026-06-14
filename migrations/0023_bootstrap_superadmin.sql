-- 0023_bootstrap_superadmin.sql
-- 引导第一个超级管理员（修 0020 引入的回归）：
-- 0020 给所有现有用户默认 role='user'，导致平台主理人也丢了管理后台入口（侧栏对非管理员隐藏）。
-- 标准的"first-admin bootstrap"：**仅当系统里还没有任何 admin/superadmin 时**，
-- 把最小 id 的账号（干净冷启动下 = 创始账号）提升为 superadmin。
-- 幂等：一旦已存在管理员，下面的 count 守卫为假 → 不动任何人；重复执行安全。
-- 子查询包一层派生表，规避 MySQL "can't specify target table for update in FROM"。
UPDATE users SET role = 'superadmin'
WHERE id = (SELECT mid FROM (SELECT MIN(id) AS mid FROM users) AS t)
  AND (SELECT cnt FROM (SELECT COUNT(*) AS cnt FROM users WHERE role IN ('admin', 'superadmin')) AS c) = 0;
