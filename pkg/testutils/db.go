package testutils

import (
	"os"
	"sync"

	"github.com/DATA-DOG/go-txdb"
	"github.com/jinzhu/gorm"
)

var dbOnce sync.Once

var Tables = []string{
	"activity_logs",
	"accounts",
	"management_clients",
	"hubs",
}

func SetupDB() {
	dbOnce.Do(func() {
		db := os.Getenv("DATABASE_URL")
		if db != "" {
			txdb.Register("pgtest", "postgres", db)
			dialect, _ := gorm.GetDialect("postgres")
			gorm.RegisterDialect("pgtest", dialect)
		}
	})
}

func CleanupDB() {
	db := os.Getenv("DATABASE_URL")
	if db != "" {
		db, err := gorm.Open("postgres", db)
		if err != nil {
			return
		}

		for _, t := range Tables {
			db.Exec("TRUNCATE " + t)
		}
	}
}
