package main

import (
	"github.com/golang-migrate/migrate/v4"
	"log"
	"os"
)

type migrateRunner struct{}

func (m *migrateRunner) Help() string {
	return "run any migrations"
}

func (m *migrateRunner) Synopsis() string {
	return "run any migrations"
}

func (mr *migrateRunner) Run(args []string) int {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		log.Fatal("no DATABASE_URL provided")
	}

	migPath := os.Getenv("MIGRATIONS_PATH")
	if migPath == "" {
		migPath = "/migrations"
	}

	m, err := migrate.New("file://"+migPath, url)
	if err != nil {
		log.Fatal(err)
	}

	err = m.Up()
	if err != nil {
		log.Fatal(err)
	}

	return 0
}