package migrations_test

import (
	"context"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"

	"{{ cookiecutter.go_module }}/internal/testdb"
	"{{ cookiecutter.go_module }}/migrations"
)

// Every migration goes up, comes back down, and goes up again — on a schema of its own, from nothing.
//
// The rest of the suite only ever migrates up, so without this file a Down section is code nobody
// has executed. Ask Athena for the database and migrations rules: a rollback you cannot execute is
// not a rollback plan.

func TestMain(m *testing.M) { testdb.Run(m) }

// The names every index and constraint must have. Postgres invents names for anything left unnamed
// (`users_pkey`); a Down section, or the next migration that alters the constraint, needs the name,
// so each one is written in the migration and pinned here.
var wantNames = []string{
	"fk_refresh_sessions_user_id_users",
	"ix_refresh_sessions_family_id",
	"ix_refresh_sessions_user_id",
	"ix_users_email",
	"pk_refresh_sessions",
	"pk_users",
}

func tables(t *testing.T, pool *pgxpool.Pool, schema string) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(),
		"SELECT table_name FROM information_schema.tables WHERE table_schema = $1 ORDER BY table_name", schema)
	if err != nil {
		t.Fatal(err)
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return names
}

func namesInDatabase(t *testing.T, pool *pgxpool.Pool, schema string) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT c.conname FROM pg_constraint c
		JOIN pg_class t ON t.oid = c.conrelid
		JOIN pg_namespace n ON n.oid = t.relnamespace
		WHERE n.nspname = $1 AND t.relname IN ('users', 'refresh_sessions')
		UNION
		SELECT indexname FROM pg_indexes
		WHERE schemaname = $1 AND tablename IN ('users', 'refresh_sessions')
		ORDER BY 1`, schema)
	if err != nil {
		t.Fatal(err)
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	return names
}

func TestEveryMigrationUpgradesDowngradesAndUpgradesAgain(t *testing.T) {
	ctx := context.Background()
	pool, schema := testdb.PoolOnNewSchema(t)
	db := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = db.Close() })
	provider, err := migrations.NewProvider(db)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("up: %v", err)
	}
	if got := tables(t, pool, schema); !slices.Contains(got, "users") || !slices.Contains(got, "refresh_sessions") {
		t.Fatalf("after up, tables = %v", got)
	}
	if got := namesInDatabase(t, pool, schema); !slices.Equal(got, wantNames) {
		t.Fatalf("constraint and index names = %v, want %v", got, wantNames)
	}

	if _, err := provider.Down(ctx); err != nil {
		t.Fatalf("down one: %v", err)
	}
	if got := tables(t, pool, schema); !slices.Contains(got, "users") || slices.Contains(got, "refresh_sessions") {
		t.Fatalf("after one step down, tables = %v; want users without refresh_sessions", got)
	}

	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("up again: %v", err)
	}
	if _, err := provider.DownTo(ctx, 0); err != nil {
		t.Fatalf("down to zero: %v", err)
	}
	if got := tables(t, pool, schema); !slices.Equal(got, []string{"goose_db_version"}) {
		t.Fatalf("after down to zero, tables = %v; want only goose's own", got)
	}
	if _, err := provider.Up(ctx); err != nil {
		t.Fatalf("up from zero: %v", err)
	}
}
