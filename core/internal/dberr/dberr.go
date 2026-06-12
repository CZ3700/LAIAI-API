// Package dberr：数据库错误判定（跨方言）。
// 生产是 MySQL——用类型化错误码判定（不依赖错误消息文本，审查 C-1）；
// 测试是 SQLite（glebarez/modernc）——保留字符串兜底（测试路径，非资金正确性来源）。
package dberr

import (
	"errors"
	"strings"

	"github.com/go-sql-driver/mysql"
)

// IsDuplicate 判定唯一约束冲突（幂等键命中）。
func IsDuplicate(err error) bool {
	if err == nil {
		return false
	}
	var me *mysql.MySQLError
	if errors.As(err, &me) {
		return me.Number == 1062 // ER_DUP_ENTRY
	}
	// SQLite（测试）：modernc 错误无稳定导出类型，按消息判定即可
	s := err.Error()
	return strings.Contains(s, "UNIQUE constraint failed") ||
		strings.Contains(s, "constraint failed: UNIQUE") ||
		strings.Contains(s, "Duplicate entry") // 兜底：任何把 MySQL 文本透传出来的情况
}
