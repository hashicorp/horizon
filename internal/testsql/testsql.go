// Package testsql provides helpers for working with PostgreSQL databases in
// unit tests, from creating per-test DBs to also running the migrations.
//
// This package should be used for all unit tests to safely create isolated
// database environments for your tests.
//
// When this package is inserted, it will introduce a `-gorm-debug` flag to
// the global flags package. This flag will turn on verbose output that logs
// all SQL statements.
package testsql

import (
	"flag"
	"fmt"
	"go/build"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/golang-migrate/migrate/v4"
	migratePostgres "github.com/golang-migrate/migrate/v4/database/postgres"
	_ "github.com/golang-migrate/migrate/v4/source/file"
	"github.com/hashicorp/go-multierror"
	"github.com/jinzhu/gorm"
	_ "github.com/jinzhu/gorm/dialects/postgres"
	"github.com/mitchellh/go-testing-interface"
)

// These variables control the database created for tests. They should not
// be modified while any active tests are running. These generally don't need
// to be modified at all.
var (
	// DefaultDBName is the default name of the database to create for tests.
	DefaultDBName = "hzn_test"

	// UserName and UserPassword are the username and password, respectively,
	// of the test user to create with access to the test database.
	UserName     = "testuser"
	UserPassword = "4555dcc0519478e4ab83fc8b78285022bdbf82da"

	// MigrationsDir is the directory path that is looked for for migrations.
	// If this is relative, then TestDB will walk parent directories until
	// this path is found and load migrations from there.
	MigrationsDir = filepath.Join("pkg", "control", "migrations")

	// postgresDBInitialized is used to indicate whether the Postgres database
	// was created and migrated at least once. This is used in combination with
	// the ReuseDB option so that databases are only reused if they are known to
	// exist and migrated before.
	//
	// This value is stored for all invocations within this process. When
	// running tests, Go splits tests into multiple binaries (i.e. one binary
	// per package). Therefore, a database is effectively reused only across
	// tests from the same package.
	postgresDBInitialized = false
)

type nopLogger struct{}

func (_ nopLogger) Print(v ...interface{}) {}

var gormDebug = flag.Bool("gorm-debug", false, "set to true to have Gorm log all generated SQL.")

// TestDBOptions collects options that customize the test databases.
type TestDBOptions struct {
	// SkipMigration allows skipping over the migration of the database.
	SkipMigration bool

	// DBReuse indicates whether the potentially existing test database can be
	// reused. If set, the database is created and migrated at least once, but
	// won't be destroyed and recreated every time one of the `Test<Type>DB`
	// functions is called.
	ReuseDB bool
}

// TestPostgresDB sets up the test DB to use, including running any migrations.
// In case the ReuseDB option is set to true, this function might not create a
// new database.
//
// This expects a local Postgres to be running with default "postgres/postgres"
// superuser credentials.
//
// This also expects that at this or some parent directory, there is the
// path represented by MigrationsDir where the migration files can be found.
// If no migrations are found, then an error is raised.
func TestPostgresDBWithOpts(t testing.T, dbName string, opts *TestDBOptions) *gorm.DB {
	t.Helper()

	// Services setting up their tests with this helper are expected to provide
	// a database name. If they don't, use a default.
	if dbName == "" {
		dbName = DefaultDBName
	}

	// Create the DB. We first drop the existing DB. The complex SQL
	// statement below evicts any connections to that database so we can
	// drop it.
	db := testDBConnectWithUser(t, "postgres", "", "postgres", "postgres")
	if *gormDebug {
		db.LogMode(true)
	}

	// If the database shouldn't be reused or if it wasn't yet initialized, drop
	// a potentially existing database, create a new one, and migrate it to the
	// latest version.
	if !opts.ReuseDB || !postgresDBInitialized {
		db.SetLogger(nopLogger{})
		// Sometimes a Postgres database can't be dropped because of some internal
		// Postgres housekeeping (Postgres runs as a collection of collaborating
		// OS processes). Before trying to drop it, terminate all other connections.
		db.Exec(`SELECT pid, pg_terminate_backend(pid)
		FROM pg_stat_activity
		WHERE datname = '` + dbName + `'
		AND pid != pg_backend_pid();`)

		db.Exec("DROP DATABASE IF EXISTS " + dbName + ";")
		db.Exec("CREATE DATABASE " + dbName + ";")
		db.Exec(fmt.Sprintf("DROP USER IF EXISTS %s;", UserName))
		db.Exec(fmt.Sprintf("CREATE USER %s WITH PASSWORD '%s';", UserName, UserPassword))
		db.Close()

		if !opts.SkipMigration {
			// Migrate using our migrations
			testMigrate(t, "postgres", dbName)
		}

		db = testDBConnectWithUser(t, "postgres", dbName, "postgres", "postgres")
		db.SetLogger(nopLogger{})
		db.Exec("GRANT ALL PRIVILEGES ON ALL TABLES IN SCHEMA public TO " + UserName + ";")
		db.Exec("GRANT ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA public TO " + UserName + ";")

		db.Close()
		postgresDBInitialized = true
	} else {
		// If the database should be reused and already exists, we truncate
		// tables.
		var tablesToTruncate []string

		db = testDBConnectWithUser(t, "postgres", dbName, "postgres", "postgres")

		// Find all user tables except the schema migrations table.
		rows, err := db.Table("pg_stat_user_tables").
			Where("relname != 'schema_migrations'").
			Rows()
		if err != nil {
			t.Errorf("unable to determine tables to truncate: %v", err)
		}
		defer rows.Close()

		var table struct {
			Relname    string
			Schemaname string
		}
		for rows.Next() {
			if err := db.ScanRows(rows, &table); err != nil {
				t.Errorf("unable to scan rows: %v", err)
			}

			// We truncate the user tables from all schemas, so prepend the
			// table name with the schema name and quote both, e.g.
			//		"public"."operations"
			tablesToTruncate = append(tablesToTruncate,
				fmt.Sprintf("%q.%q", table.Schemaname, table.Relname),
			)
		}

		if len(tablesToTruncate) > 0 {
			// By truncating all tables within the same query, foreign key
			// constraints don't break.
			db = db.Exec(fmt.Sprintf("TRUNCATE %s;", strings.Join(tablesToTruncate, ",")))
			if errs := db.GetErrors(); len(errs) > 0 {
				t.Errorf("failed to truncate tables: %v",
					multierror.Append(nil, errs...),
				)
			}
		}
		db.Close()
	}

	return TestDBConnect(t, "postgres", dbName)
}

// TestPostgresDB sets up the test DB to use, including running any migrations.
//
// This expects a local Postgres to be running with default "postgres/postgres"
// superuser credentials.
//
// This also expects that at this or some parent directory, there is the
// path represented by MigrationsDir where the migration files can be found.
// If no migrations are found, then an error is raised.
func TestPostgresDB(t testing.T, dbName string) *gorm.DB {
	return TestPostgresDBWithOpts(t, dbName, &TestDBOptions{})
}

// TestPostgresDBString returns the connection string for the database.
// This is safe to call alongside TestPostgresDB. This won't be valid until
// TestPostgresDB is called.
func TestPostgresDBString(t testing.T, dbName string) string {
	return testDBConnectWithUserString(t, "postgres", dbName, UserName, UserPassword)
}

// TestDBConnect connects to the local test database but does not recreate it.
func TestDBConnect(t testing.T, family, dbName string) *gorm.DB {
	return testDBConnectWithUser(t, family, dbName, UserName, UserPassword)
}

// TestDBConnectSuper connects to the local database as a super user but does
// not recreate it.
func TestDBConnectSuper(t testing.T, family, dbName string) *gorm.DB {
	return testDBConnectWithUser(t, family, dbName, "root", "root")
}

// TestDBCommit commits a query and verifies it succeeds. This function is
// useful for one-liners in tests to load data or make changes. For example:
//
//     var count int
//     TestDBCommit(t, db.Begin().Table("foos").Count(&count))
//
func TestDBCommit(t testing.T, db *gorm.DB) {
	t.Helper()

	if errs := db.Commit().GetErrors(); len(errs) > 0 {
		err := multierror.Append(nil, errs...)
		t.Fatalf("err: %s", err)
	}
}

// TestDBSave saves a changed model.
func TestDBSave(t testing.T, db *gorm.DB, m interface{}) {
	t.Helper()

	q := db.Begin().Save(m).Commit()
	if errs := q.GetErrors(); len(errs) > 0 {
		err := multierror.Append(nil, errs...)
		t.Fatalf("err: %s", err)
	}
}

func testDBConnectWithUser(t testing.T, family, database, user, pass string) *gorm.DB {
	t.Helper()

	db, err := gorm.Open("postgres",
		fmt.Sprintf("host=127.0.0.1 port=5432 sslmode=disable user=%s password=%s dbname=%s", user, pass, database))
	if err != nil {
		t.Fatalf("err: %s", err)
	}

	// BlockGlobalUpdate - Error on update/delete without where clause
	// since it's usually a bug to not have any filters when querying models.
	//
	// By default, GORM will not include zero values in where clauses.
	// This setting may help prevent bugs or missing validation causing major data corruption.
	//
	// For example:
	//
	//	var result models.Deployment
	//	db.Model(models.Deployment{}).
	//		Where(models.Deployment{ClusterID: sqluuid.UUID{}}).
	//		First(&result)
	//
	//	Results in this query:
	//	SELECT * FROM `consul_deployments` ORDER BY `consul_deployments`.`number` ASC LIMIT 1
	//
	//  Which effectively picks a random deployment.
	db.BlockGlobalUpdate(true)

	if *gormDebug {
		db.LogMode(true)
	}

	return db
}

func testDBConnectWithUserString(t testing.T, family, database, user, pass string) string {
	return fmt.Sprintf("host=127.0.0.1 port=5432 sslmode=disable user=%s password=%s dbname=%s",
		user, pass, database)
}

// testMigrate migrates the current database.
func testMigrate(t testing.T, family, dbName string) {
	t.Helper()

	// Find the path to the migrations. We do this using a heuristic
	// of just searching up directories until we find
	// "models/migrations". This assumes any tests run
	// will be a child of the root folder.
	dir := testMigrateDir(t)

	db := testDBConnectWithUser(t, "postgres", dbName, "postgres", "postgres")
	defer db.Close()
	driver, err := migratePostgres.WithInstance(db.DB(), &migratePostgres.Config{
		MigrationsTable: "hzn_test_migrations",
	})
	if err != nil {
		t.Fatalf("err: %s", err)
	}

	// Creator the migrator
	migrator, err := migrate.NewWithDatabaseInstance(
		"file://"+dir, family, driver)
	if err != nil {
		t.Fatalf("err: %s", err)
	}
	defer migrator.Close()

	// Enable logging
	if *gormDebug {
		migrator.Log = &migrateLogger{t: t}
	}

	// Migrate
	if err := migrator.Up(); err != nil {
		t.Fatalf("err migrating: %s", err)
	}
}

// testMigrateDir attempts to find the directory with migrations. This will
// search the working directory and parents first, then will fall back to
// the Go Modules directory.
func testMigrateDir(t testing.T) string {
	search := func(root string) string {
		for {
			current := filepath.Join(root, MigrationsDir)
			_, err := os.Stat(current)
			if err == nil {
				// Found it!
				return current
			}
			if err != nil && !os.IsNotExist(err) {
				t.Fatalf("error at %s: %s", root, err)
			}

			// Traverse to parent
			next := filepath.Dir(root)
			if root == next {
				return ""
			}
			root = next
		}
	}

	// Search our working directory first
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("err getting working dir: %s", err)
	}
	if v := search(dir); v != "" {
		return v
	}

	// Search for a gomod directory
	dir = gomodDir(t)
	if dir != "" {
		return search(dir)
	}

	return ""
}

// gomodDir finds the Horizon module with the latest version.
func gomodDir(t testing.T) string {
	// Get the first GOPATH element
	gopath := build.Default.GOPATH
	if idx := strings.Index(gopath, ":"); idx != -1 {
		gopath = gopath[:idx]
	}
	if gopath == "" {
		return ""
	}

	// Open the directory to list all modules for HashiCorp
	root := filepath.Join(gopath, "pkg", "mod", "github.com", "hashicorp")
	dirh, err := os.Open(root)
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}

		t.Fatalf("error looking for migrations: %s", err)
	}
	defer dirh.Close()

	// Read all the directories and sort them
	names, err := dirh.Readdirnames(-1)
	if err != nil {
		t.Fatalf("error looking for migrations: %s", err)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(names)))

	// Find the horizon one
	for _, n := range names {
		if strings.HasPrefix(n, "horizon@") {
			return filepath.Join(root, n)
		}
	}

	return ""
}

// migrateLogger implements migrate.Logger so that we can have logging
// on migrations when requested.
type migrateLogger struct{ t testing.T }

func (m *migrateLogger) Printf(format string, v ...interface{}) {
	m.t.Logf(format, v...)
}

func (m *migrateLogger) Verbose() bool {
	return true
}

// TestAssertCount is a helper for asserting the expected number of rows exist
// in the DB. It requires that the db argument is passed a *gorm.DB that must
// already have had a Table selection and optionally a where clause added to
// specify what to count. This helper will run `Count()` on the db passed and
// assert it succeeds and finds the desired number of records.
// Examples:
//   // Assert foo is empty
//   models.TestAssertCount(t, db.Table("foo"), 0)
//   // Assert 3 providers exist for a given module
//   models.TestAssertCount(t,
//   	db.Model(&models.ModuleProvider{}).Where("provider = ?", provider),
//		3)
func TestAssertCount(t testing.T, db *gorm.DB, want int) {
	t.Helper()

	count := 0
	// Assume DB already describes a query that selects the rows required
	db.Count(&count)
	if errs := db.GetErrors(); len(errs) > 0 {
		err := multierror.Append(nil, errs...)
		t.Fatalf("failed counting rows: %s", err)
	}

	if want != count {
		t.Fatalf("got %d rows, want %d", count, want)
	}
}
