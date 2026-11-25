package server

import "gorm.io/gorm"

func sqlDateExpr(db *gorm.DB, col string) string {
	if db != nil && db.Dialector != nil && db.Dialector.Name() == "postgres" {
		return "TO_CHAR(" + col + " AT TIME ZONE 'UTC', 'YYYY-MM-DD')"
	}
	return "DATE(" + col + ")"
}

func sqlHourExpr(db *gorm.DB, col string) string {
	if db != nil && db.Dialector != nil && db.Dialector.Name() == "postgres" {
		return "TO_CHAR(" + col + " AT TIME ZONE 'UTC', 'YYYY-MM-DD HH24:00')"
	}
	return "strftime('%Y-%m-%d %H:00', " + col + ")"
}
