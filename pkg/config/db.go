package config

import (
	"os"
	"sync"

	"github.com/jinzhu/gorm"
)

var (
	dbOnce sync.Once
	db     *gorm.DB
)

func DB() *gorm.DB {
	dbOnce.Do(func() {
		if db == nil {
			connect := os.Getenv("DATABASE_URL")
			if connect == "" {
				panic("no database configured")
			}

			x, err := gorm.Open("postgres", connect)
			if err != nil {
				panic(err)
			}

			db = x
		}
	})

	if db == nil {
		panic("no database configured")

	}
	return db
}
