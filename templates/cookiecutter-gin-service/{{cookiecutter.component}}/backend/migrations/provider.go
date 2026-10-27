package migrations

import (
	"database/sql"
	"fmt"

	"github.com/pressly/goose/v3"
)

// NewProvider is goose over the embedded migrations, for the given database. The migrate
// subcommand and the test harness both build theirs here, so the suite migrates exactly the way
// production does.
//
// Each migration runs in its own transaction (goose's default), which is what makes the
// `SET LOCAL lock_timeout` at the top of every file mean something. A migration that cannot run in a
// transaction — CREATE INDEX CONCURRENTLY — says so with `-- +goose NO TRANSACTION`.
func NewProvider(db *sql.DB) (*goose.Provider, error) {
	provider, err := goose.NewProvider(goose.DialectPostgres, db, FS)
	if err != nil {
		return nil, fmt.Errorf("load migrations: %w", err)
	}
	return provider, nil
}
