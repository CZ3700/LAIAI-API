package migrate

import (
	"fmt"
	"io/fs"
	"regexp"
	"sort"
)

// MySQL DDL 安全背景（P1-7）：MySQL 的 DDL（CREATE/ALTER/DROP …）会**隐式提交**且
// **不可回滚**。因此 Up 的 db.Transaction 在 MySQL 上对「单文件多条 DDL」并不保证原子
// （在 SQLite 测试方言下 DDL 才是事务性的，故单测能验回滚——但生产 MySQL 不然）。
// 后果：多 DDL 文件若第 k 条失败，前 k-1 条已落地，schema_migrations 却未记录该版本
// → 半截 schema，re-run 撞「已存在」而卡死，须人工介入。
//
// 纪律（向后）：
//   - 一个迁移文件尽量只含一条 DDL（单条 DDL 自身原子，失败=零落地，re-run 干净）；
//   - 多列新增务必合并为**单条** ALTER TABLE t ADD COLUMN a…, ADD COLUMN b…（一次原子 DDL）；
//   - 破坏性/类型变更 DDL（DROP/TRUNCATE/MODIFY/CHANGE/RENAME）必须先备份 + 人工 runbook。
// Lint 把以上编码为静态检查，schema-doctor 与 CI smoke 消费它。

// LintFinding 一条迁移安全告警。
type LintFinding struct {
	File     string
	Stmt     int    // 1-based 语句序号；0 = 文件级
	Severity string // "danger"（破坏性/数据丢失，CI gate 拦截）| "warn"（MySQL 非原子风险）
	Rule     string
	Detail   string
}

func (f LintFinding) String() string {
	loc := f.File
	if f.Stmt > 0 {
		loc = fmt.Sprintf("%s stmt %d", f.File, f.Stmt)
	}
	return fmt.Sprintf("[%s] %s — %s (%s)", f.Severity, loc, f.Detail, f.Rule)
}

var (
	reDestructive = regexp.MustCompile(`(?i)(\bDROP\s+TABLE\b|\bDROP\s+COLUMN\b|\bTRUNCATE\b|\bMODIFY\s+COLUMN\b|\bCHANGE\s+COLUMN\b|\bRENAME\s+TABLE\b|\bRENAME\s+COLUMN\b|\bALTER\s+TABLE\s+\S+\s+DROP\b)`)
	reDDL         = regexp.MustCompile(`(?i)^(CREATE\s+TABLE|ALTER\s+TABLE|CREATE\s+(UNIQUE\s+)?INDEX|DROP\s+INDEX|CREATE\s+VIEW|DROP\s+VIEW)\b`)
	reAddColumn   = regexp.MustCompile(`(?i)\bALTER\s+TABLE\b.*\bADD\s+COLUMN\b`)
	// 显式确认标记：迁移文件含 `-- migrate:ack-destructive <理由>` 时，其破坏性 DDL 降级为
	// "acknowledged"（已人工 runbook 评审），不再 fail CI gate。用于必要的破坏性变更（如分片改唯一键），
	// 杜绝意外破坏性 DDL 漏网——必须有意识写入标记 + 留理由。
	reAckDestructive = regexp.MustCompile(`(?i)migrate:ack-destructive`)
)

// Lint 静态扫描所有迁移的 MySQL 安全风险。返回的 findings 按文件名序排列。
//
//	danger：破坏性/类型变更 DDL —— 须人工备份 + runbook，CI gate 应拦截。
//	warn  ：单文件多条 DDL（MySQL 隐式提交、非原子）—— 建议拆分 / 合并 ADD COLUMN。
func Lint(fsys fs.FS) ([]LintFinding, error) {
	files, err := fs.Glob(fsys, "*.sql")
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	var out []LintFinding
	for _, f := range files {
		raw, err := fs.ReadFile(fsys, f)
		if err != nil {
			return nil, fmt.Errorf("lint: read %s: %w", f, err)
		}
		stmts := splitStatements(string(raw))
		acked := reAckDestructive.Match(raw) // 文件级显式确认标记
		ddlCount, addColCount := 0, 0
		for i, s := range stmts {
			if reDestructive.MatchString(s) {
				sev, detail := "danger", "破坏性/类型变更 DDL，须先备份 + 人工 runbook 确认"
				if acked {
					sev, detail = "ack", "破坏性 DDL 已显式确认（migrate:ack-destructive，已人工 runbook 评审）"
				}
				out = append(out, LintFinding{
					File: f, Stmt: i + 1, Severity: sev, Rule: "destructive-ddl", Detail: detail,
				})
			}
			if reDDL.MatchString(s) {
				ddlCount++
			}
			if reAddColumn.MatchString(s) {
				addColCount++
			}
		}
		if ddlCount > 1 {
			detail := fmt.Sprintf("单文件 %d 条 DDL；MySQL 下 DDL 隐式提交、非原子，中途失败留半截 schema、re-run 撞已存在", ddlCount)
			if addColCount > 1 {
				detail += "；多条 ADD COLUMN 建议合并为单条 ALTER TABLE … ADD …, ADD …（一次原子 DDL）"
			}
			out = append(out, LintFinding{File: f, Severity: "warn", Rule: "multi-ddl-non-atomic", Detail: detail})
		}
	}
	return out, nil
}

// HasDanger 报告 findings 是否含 danger 级（CI gate 据此 fail）。
func HasDanger(findings []LintFinding) bool {
	for _, f := range findings {
		if f.Severity == "danger" {
			return true
		}
	}
	return false
}

var reCreateTable = regexp.MustCompile("(?i)^CREATE\\s+TABLE\\s+(?:IF\\s+NOT\\s+EXISTS\\s+)?`?([A-Za-z0-9_]+)`?")

// CreatedTables 解析所有迁移里 CREATE TABLE 的表名（即 schema 期望存在的表集），
// 去重、按名排序。schema-doctor 据此探测线上是否被迁移外手段删表（漂移）。
func CreatedTables(fsys fs.FS) ([]string, error) {
	files, err := fs.Glob(fsys, "*.sql")
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	seen := map[string]bool{}
	var out []string
	for _, f := range files {
		raw, err := fs.ReadFile(fsys, f)
		if err != nil {
			return nil, fmt.Errorf("createdtables: read %s: %w", f, err)
		}
		for _, s := range splitStatements(string(raw)) {
			if m := reCreateTable.FindStringSubmatch(s); m != nil {
				if name := m[1]; !seen[name] {
					seen[name] = true
					out = append(out, name)
				}
			}
		}
	}
	sort.Strings(out)
	return out, nil
}
