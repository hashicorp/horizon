package config

import (
	"os"
	"strings"
	"sync"

	"github.com/jinzhu/gorm"
	"github.com/mitchellh/go-testing-interface"

	"github.com/hashicorp/horizon/internal/testsql"
)

var (
	dbOnce sync.Once
	db     *gorm.DB
)

var (
	TestDBUrl = "postgres://localhost/horizon_test?sslmode=disable"
	DevDBUrl  = "postgres://localhost/horizon_dev?sslmode=disable"
)

func DB() *gorm.DB {
	dbOnce.Do(func() {
		if db != nil {
			return
		}

		// If we're in a unit test, use the test framework to create the
		// DB. This is safe even if the heuristic is wrong because we always
		// create a new test database. So anything in DATABASE_URL is safe.
		if strings.HasSuffix(os.Args[0], ".test") {
			db = testsql.TestPostgresDB(&testing.RuntimeT{}, "hzn_config")
			return
		}

		connect := os.Getenv("DATABASE_URL")
		if connect == "" {
			panic("DATABASE_URL must be set")
		}

		x, err := gorm.Open("postgres", connect)
		if err != nil {
			panic(err)
		}

		db = x
	})

	if db == nil {
		panic("no database configured")

	}
	return db
}
