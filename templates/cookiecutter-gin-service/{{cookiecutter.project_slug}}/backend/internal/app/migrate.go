package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"{{ cookiecutter.go_module }}/internal/config"
	"{{ cookiecutter.go_module }}/migrations"
)

// Migrate runs `migrate up|down|status` against the configured database.
//
// The deployed migrate job is this same image with args `migrate up`, run before the new revision
// takes traffic — so every migration must be backward-compatible with the code already serving. Ask
// Athena for the database access and migrations rules for what that has to mean.
//
// The job is granted the database and nothing else: no signing key, no Sentry DSN. That is why the
// signing-key check lives where the router is built, not in config.Load, which this calls.
func Migrate(ctx context.Context, cfg config.Config, logger *slog.Logger, args []string, out io.Writer) error {
	if len(args) != 1 {
		return errors.New("usage: api migrate up|down|status")
	}
	connConfig, err := pgx.ParseConfig(cfg.DatabaseURL())
	if err != nil {
		// Never wrap the parse error itself: it can quote the DSN, password included.
		return errors.New("DATABASE_URL_OVERRIDE / POSTGRES_* do not form a valid connection string")
	}
	db := stdlib.OpenDB(*connConfig)
	defer func() { _ = db.Close() }()

	provider, err := migrations.NewProvider(db)
	if err != nil {
		return err
	}

	switch args[0] {
	case "up":
		results, err := provider.Up(ctx)
		for _, result := range results {
			logger.InfoContext(ctx, "migration_applied", "version", result.Source.Version, "file", result.Source.Path, "duration_ms", result.Duration.Milliseconds())
		}
		if err != nil {
			return fmt.Errorf("migrate up: %w", err)
		}
		if len(results) == 0 {
			logger.InfoContext(ctx, "migrations_up_to_date")
		}
	case "down":
		// One step, never "to zero": rolling back is a deliberate, reviewed act per migration.
		result, err := provider.Down(ctx)
		if err != nil {
			return fmt.Errorf("migrate down: %w", err)
		}
		logger.InfoContext(ctx, "migration_rolled_back", "version", result.Source.Version, "file", result.Source.Path)
	case "status":
		statuses, err := provider.Status(ctx)
		if err != nil {
			return fmt.Errorf("migrate status: %w", err)
		}
		for _, status := range statuses {
			applied := "pending"
			if !status.AppliedAt.IsZero() {
				applied = status.AppliedAt.UTC().Format("2006-01-02 15:04:05Z")
			}
			if _, err := fmt.Fprintf(out, "%-8d %-22s %s\n", status.Source.Version, applied, status.Source.Path); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unknown migrate command %q: want up, down or status", args[0])
	}
	return nil
}
