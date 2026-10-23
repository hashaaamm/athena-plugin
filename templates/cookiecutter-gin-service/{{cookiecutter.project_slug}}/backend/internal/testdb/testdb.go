// Package testdb is the test suite's database: a real Postgres, never a substitute.
//
// Three decisions, and they are what make `go test -race ./...` safe:
//
//  1. A real Postgres. A different dialect finds different bugs, and an in-memory fake finds none of
//     the ones that matter here — row locks, unique-index races, transactional DDL.
//  2. One schema per test package. `go test ./...` runs packages as separate processes, in parallel;
//     without structural isolation they race on the same tables and the failures look random. Each
//     package's TestMain calls Run, which creates a schema with a random name, migrates it with the
//     real migrations, and points every pooled connection at it through `search_path` — so no query
//     needs to know.
//  3. One transaction per test, rolled back. Tx begins a transaction the test hands to
//     repository.New; every Store.WithTx inside the code under test becomes a SAVEPOINT within it,
//     so the real commit-or-rollback logic runs unmodified and the test still leaves nothing behind.
//
// The connection comes from the same configuration the service reads (POSTGRES_* or
// DATABASE_URL_OVERRIDE): the compose test-runner and the CI job both set it. Run the suite with
// `just test` rather than a bare `go test` on the host, unless you have exported those yourself.
package testdb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"

	"{{ cookiecutter.go_module }}/internal/config"
	"{{ cookiecutter.go_module }}/migrations"
)

var (
	pool   *pgxpool.Pool
	schema string
)

// Run is a package's TestMain body: it creates and migrates the package's schema, runs the tests,
// and drops the schema. Returning from TestMain exits with m.Run's code.
//
//	func TestMain(m *testing.M) { testdb.Run(m) }
func Run(m *testing.M) {
	if err := setUp(); err != nil {
		fmt.Fprintln(os.Stderr, "testdb:", err)
		os.Exit(1)
	}
	defer tearDown()
	m.Run()
}

// Pool is the package's pool, every connection on its schema. For the tests that need more than one
// connection — the row-lock test — and must therefore commit, and clean up after themselves.
func Pool() *pgxpool.Pool { return pool }

// Schema is the package's schema name.
func Schema() string { return schema }

// Config is the configuration the suite connects with.
func Config(t testing.TB) config.Config {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("load configuration: %v", err)
	}
	return cfg
}

// Tx is a transaction on the package's schema, rolled back when the test ends.
func Tx(t testing.TB) pgx.Tx {
	t.Helper()
	tx, err := pool.Begin(context.Background())
	if err != nil {
		t.Fatalf("begin test transaction: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	return tx
}

// PoolOnNewSchema is a pool on a fresh, empty schema of its own, dropped when the test ends. For the
// migration test, which must start from nothing.
func PoolOnNewSchema(t testing.TB) (*pgxpool.Pool, string) {
	t.Helper()
	ctx := context.Background()
	name, err := randomSchema()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{name}.Sanitize()); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	scoped, err := poolFor(ctx, name)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		scoped.Close()
		_, _ = pool.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+pgx.Identifier{name}.Sanitize()+" CASCADE")
	})
	return scoped, name
}

func setUp() error {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	name, err := randomSchema()
	if err != nil {
		return err
	}
	admin, err := poolFor(ctx, "")
	if err != nil {
		return err
	}
	_, err = admin.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{name}.Sanitize())
	admin.Close()
	if err != nil {
		return fmt.Errorf("create schema (is Postgres reachable? run the suite with `just test`): %w", err)
	}
	schema = name
	pool, err = poolFor(ctx, name)
	if err != nil {
		return err
	}

	db := stdlib.OpenDBFromPool(pool)
	defer func() { _ = db.Close() }()
	provider, err := migrations.NewProvider(db)
	if err != nil {
		return err
	}
	if _, err := provider.Up(ctx); err != nil {
		return fmt.Errorf("migrate test schema: %w", err)
	}
	return nil
}

func tearDown() {
	if pool == nil {
		return
	}
	_, _ = pool.Exec(context.Background(), "DROP SCHEMA IF EXISTS "+pgx.Identifier{schema}.Sanitize()+" CASCADE")
	pool.Close()
}

// poolFor builds a pool whose connections all use the given schema ("" for the server default).
func poolFor(ctx context.Context, name string) (*pgxpool.Pool, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, fmt.Errorf("load configuration: %w", err)
	}
	poolConfig, err := pgxpool.ParseConfig(cfg.DatabaseURL())
	if err != nil {
		return nil, fmt.Errorf("parse database URL: %w", err)
	}
	if name != "" {
		poolConfig.ConnConfig.RuntimeParams["search_path"] = name
	}
	return pgxpool.NewWithConfig(ctx, poolConfig)
}

func randomSchema() (string, error) {
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		return "", err
	}
	return "test_" + hex.EncodeToString(suffix), nil
}
