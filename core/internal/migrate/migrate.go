// Package migrate 是版本化 SQL 迁移执行器（设计文档红线 5：本平台永久禁用 AutoMigrate）。
//
// 约定：
//   - 迁移文件命名 NNNN_name.sql（NNNN 递增，字典序即执行序），存于仓库 migrations/
//   - 每个文件在单事务内执行；失败整体回滚，已应用版本记录在 schema_migrations
//   - 文件内语句以 ";" 结尾分割；**字符串字面量中禁止出现分号**（项目纪律，换取无解析器依赖）
//   - 只有 Up，没有 Down——回滚靠备份恢复（旧平台 Migration runbook 决策延续）
package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	"gorm.io/gorm"
)

// advisoryLockName / advisoryLockTimeout：多副本并发迁移的 MySQL 会话级互斥（WS-3）。
const (
	advisoryLockName    = "laiai_migrate"
	advisoryLockTimeout = 120 // 秒：足够最慢的一批迁移跑完；超时视为另一实例迁移中（entrypoint 会重试）
)

type Record struct {
	Version   string `gorm:"primaryKey;column:version"`
	AppliedAt string `gorm:"column:applied_at"`
}

func (Record) TableName() string { return "schema_migrations" }

// Up 应用 fsys 根目录下所有未应用的 *.sql 迁移，按文件名字典序。返回本次新应用的版本列表。
//
// 多副本安全（WS-3）：MySQL 上整个迁移流程用会话级 GET_LOCK 串行化——N 个 core 副本同时启动时
// 只有一个进程执行迁移，其余阻塞到它释放后再跑（届时迁移已全部 applied，各自空过）。锁持在一条
// 专用连接上直到显式 RELEASE_LOCK（GET_LOCK 是连接/会话级；conn.Close 只归还连接池不结束会话，
// 故必须显式释放——对抗评审命中点）。SQLite 测试方言无 advisory lock，靠单连接串行兜底。
func Up(db *gorm.DB, fsys fs.FS) ([]string, error) {
	if db.Dialector.Name() != "mysql" {
		return up(db, fsys)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return nil, fmt.Errorf("migrate: db handle: %w", err)
	}
	ctx := context.Background()
	conn, err := sqlDB.Conn(ctx) // 专用连接：锁随此连接的会话存活，直到 RELEASE_LOCK
	if err != nil {
		return nil, fmt.Errorf("migrate: dedicated conn: %w", err)
	}
	defer conn.Close()
	var got sql.NullInt64
	if err := conn.QueryRowContext(ctx,
		fmt.Sprintf("SELECT GET_LOCK('%s', %d)", advisoryLockName, advisoryLockTimeout)).Scan(&got); err != nil {
		return nil, fmt.Errorf("migrate: acquire advisory lock: %w", err)
	}
	if !got.Valid || got.Int64 != 1 {
		// 超时（另一实例正在迁移且 >timeout）或异常：报错让 entrypoint 重试，绝不在未持锁时迁移。
		return nil, fmt.Errorf("migrate: could not acquire advisory lock %q within %ds (another instance migrating?)",
			advisoryLockName, advisoryLockTimeout)
	}
	defer conn.ExecContext(ctx, fmt.Sprintf("SELECT RELEASE_LOCK('%s')", advisoryLockName)) //nolint:errcheck
	return up(db, fsys)
}

// up 是迁移主体（被 Up 在持有 advisory lock 后调用；SQLite 直接调）。
func up(db *gorm.DB, fsys fs.FS) ([]string, error) {
	if err := ensureTable(db); err != nil {
		return nil, err
	}
	files, err := fs.Glob(fsys, "*.sql")
	if err != nil {
		return nil, err
	}
	sort.Strings(files)

	applied := map[string]bool{}
	var records []Record
	if err := db.Find(&records).Error; err != nil {
		return nil, fmt.Errorf("migrate: read schema_migrations: %w", err)
	}
	for _, r := range records {
		applied[r.Version] = true
	}

	var ran []string
	for _, f := range files {
		version := strings.TrimSuffix(f, ".sql")
		if applied[version] {
			continue
		}
		raw, err := fs.ReadFile(fsys, f)
		if err != nil {
			return nil, fmt.Errorf("migrate: read %s: %w", f, err)
		}
		stmts := splitStatements(string(raw))
		if len(stmts) == 0 {
			return nil, fmt.Errorf("migrate: %s contains no statements", f)
		}
		// ⚠️ MySQL 上 DDL 隐式提交、不可回滚：本事务对「单文件多条 DDL」不保证原子
		// （仅 SQLite 测试方言下 DDL 才事务性）。安全靠 Lint（破坏性拦截 + 多 DDL 告警）+
		// 纪律（单文件单 DDL / 多列合并为单条 ALTER）兜底，见 lint.go。
		err = db.Transaction(func(tx *gorm.DB) error {
			for i, s := range stmts {
				if err := tx.Exec(s).Error; err != nil {
					return fmt.Errorf("migrate: %s statement %d: %w", f, i+1, err)
				}
			}
			return tx.Exec("INSERT INTO schema_migrations (version, applied_at) VALUES (?, CURRENT_TIMESTAMP)", version).Error
		})
		if err != nil {
			return ran, err
		}
		ran = append(ran, version)
	}
	return ran, nil
}

// Status 返回 (已应用, 待应用)。
func Status(db *gorm.DB, fsys fs.FS) (applied []string, pending []string, err error) {
	if err := ensureTable(db); err != nil {
		return nil, nil, err
	}
	var records []Record
	if err := db.Order("version").Find(&records).Error; err != nil {
		return nil, nil, err
	}
	seen := map[string]bool{}
	for _, r := range records {
		applied = append(applied, r.Version)
		seen[r.Version] = true
	}
	files, err := fs.Glob(fsys, "*.sql")
	if err != nil {
		return nil, nil, err
	}
	sort.Strings(files)
	for _, f := range files {
		v := strings.TrimSuffix(f, ".sql")
		if !seen[v] {
			pending = append(pending, v)
		}
	}
	return applied, pending, nil
}

func ensureTable(db *gorm.DB) error {
	// 方言通用的最小表结构（MySQL 与 SQLite 均可执行）
	return db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		version VARCHAR(128) NOT NULL PRIMARY KEY,
		applied_at VARCHAR(64) NOT NULL
	)`).Error
}

// splitStatements 去掉 -- 行注释后按 ";" 切分。
func splitStatements(src string) []string {
	var sb strings.Builder
	for _, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "--") {
			continue
		}
		// 行内 -- 注释（前提：字符串字面量内无 "--"，项目纪律内可接受）
		if idx := strings.Index(line, "--"); idx >= 0 {
			line = line[:idx]
		}
		sb.WriteString(line)
		sb.WriteString("\n")
	}
	parts := strings.Split(sb.String(), ";")
	var out []string
	for _, p := range parts {
		if s := strings.TrimSpace(p); s != "" {
			out = append(out, s)
		}
	}
	return out
}
