-- 0025_admin_remark.sql
-- 用户备注（gap P2-B，对标 NewAPI 用户「备注」列）：运营给用户加私有备注，仅后台可见可改。
-- 纯运营字段，不参与任何业务逻辑/计费；不外露给用户自身（仅 admin 接口返回）。
ALTER TABLE users ADD COLUMN admin_remark VARCHAR(500) NOT NULL DEFAULT '';
