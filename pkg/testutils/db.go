package testutils

import (
	"os"
	"sync"

	"github.com/DATA-DOG/go-txdb"
	"github.com/hashicorp/horizon/pkg/config"
	"github.com/jinzhu/gorm"
)

var dbOnce sync.Once

var Tables = []string{
	"activity_logs",
	"accounts",
	"management_clients",
	"hubs",
}

var DbUrl = config.TestDBUrl

func SetupDB() {
	dbOnce.Do(func() {
		url := os.Getenv("DATABASE_URL")
		if url != "" {
			DbUrl = url
		}

		txdb.Register("pgtest", "postgres", DbUrl)
		dialect, _ := gorm.GetDialect("postgres")
		gorm.RegisterDialect("pgtest", dialect)
	})
}

func CleanupDB() {
	db, err := gorm.Open("postgres", DbUrl)
	if err != nil {
		return
	}

	for _, t := range Tables {
		db.Exec("TRUNCATE " + t)
	}
}
