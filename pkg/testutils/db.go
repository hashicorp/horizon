package testutils

import (
	"os"
	"sync"

	"github.com/DATA-DOG/go-txdb"
	"github.com/jinzhu/gorm"
)

var dbOnce sync.Once

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
