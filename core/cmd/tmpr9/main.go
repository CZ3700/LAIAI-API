package main

import (
	"fmt"
	"os"

	"github.com/laiai/laiai-next/core/internal/arkpricetemplate"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func main() {
	db, err := gorm.Open(mysql.Open(os.Getenv("LAIAI_DSN")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		fmt.Println("DB_ERR:", err)
		return
	}
	err = db.Transaction(func(tx *gorm.DB) error {
		tpl, e := arkpricetemplate.Load(tx, "seedance-2.5-flash", "seedance-20261105-v1")
		if e != nil {
			return e
		}
		fmt.Println("OK cells:", len(tpl.Cells), "min:", tpl.MinimumCellCount)
		return nil
	})
	if err != nil {
		fmt.Println("TX_ERR:", err)
	}
}
