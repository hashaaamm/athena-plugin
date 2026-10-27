package app

import (
	"context"
	"database/sql"
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
//
// Deployed, it connects as the admin login — the schema's owner — while the service connects as its
// own IAM identity with data privileges only. `up` therefore ends by bootstrapping that identity:
// see bootstrapAppRole.
func Migrate(ctx context.Context, cfg config.Config, logger *slog.Logger, args []string, out io.Writer) error {
	if len(args) != 1 {
		return errors.New("usage: api migrate up|down|status")
	}
	pool, closePool, err := openPool(ctx, cfg)
	if err != nil {
		return err
	}
	defer closePool()
	db := stdlib.OpenDBFromPool(pool)
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
		// After the migrations, on every run — up to date or not — so a table a migration just
		// created is covered, and a grant lost by hand is restored by the next deploy.
		if cfg.DatabaseAppRole != "" {
			if err := bootstrapAppRole(ctx, db, cfg.DatabaseAppRole, cfg.DatabaseAppAuth); err != nil {
				return fmt.Errorf("bootstrap the application's database role: %w", err)
			}
			logger.InfoContext(ctx, "app_role_granted", "role", cfg.DatabaseAppRole, "auth", cfg.DatabaseAppAuth)
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

// appGrants is what the application's role may do in the schema the migrations ran in: read and
// write rows and use sequences — nothing that changes the schema. %[1]s is the schema and %[2]s the
// role, both already quoted.
//
// ALTER DEFAULT PRIVILEGES applies to objects the CURRENT role creates, which is the migrator — the
// role that runs every future migration — so a table added next month is usable without anybody
// remembering a GRANT. The explicit grants cover what already exists, the default ones what does not
// yet.
var appGrants = []string{
	"GRANT USAGE ON SCHEMA %[1]s TO %[2]s",
	"GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA %[1]s TO %[2]s",
	"GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA %[1]s TO %[2]s",
	"ALTER DEFAULT PRIVILEGES IN SCHEMA %[1]s GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO %[2]s",
	"ALTER DEFAULT PRIVILEGES IN SCHEMA %[1]s GRANT USAGE, SELECT ON SEQUENCES TO %[2]s",
}

// bootstrapAppRole makes the application's database identity exist and hold appGrants, in one
// transaction. Every statement in it is safe to run again, which is why it runs on every deploy.
//
// How the role comes to exist is the one thing that differs by cloud:
//
//   - rds-iam: the role is an ordinary login this creates, then made a member of rds_iam, which is
//     what tells RDS to accept an IAM token instead of a password for it. It never has a password.
//   - cloud-sql-iam: the stack creates the role, as a Cloud SQL IAM user named for the service
//     account, because only Cloud SQL can make a role that authenticates with IAM. This refuses to
//     create one: a plain role of that name would stop the stack creating the real one.
//
// Both names are quoted by pgx. A Cloud SQL IAM user is `name@project.iam`, which is not a valid
// bare identifier.
func bootstrapAppRole(ctx context.Context, db *sql.DB, role, auth string) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	quoted := pgx.Identifier{role}.Sanitize()
	var exists bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)", role).Scan(&exists); err != nil {
		return err
	}
	switch auth {
	case config.AuthRDSIAM:
		if !exists {
			if _, err := tx.ExecContext(ctx, "CREATE ROLE "+quoted+" WITH LOGIN"); err != nil {
				return fmt.Errorf("create role %s: %w", quoted, err)
			}
		}
		if _, err := tx.ExecContext(ctx, "GRANT rds_iam TO "+quoted); err != nil {
			return fmt.Errorf("grant rds_iam to %s: %w", quoted, err)
		}
	case config.AuthCloudSQLIAM:
		if !exists {
			return fmt.Errorf("role %s does not exist: the stack creates it as a Cloud SQL IAM user, so run the stack's `up` before migrating", quoted)
		}
	default:
		return fmt.Errorf("DATABASE_APP_AUTH %q: want %s or %s", auth, config.AuthRDSIAM, config.AuthCloudSQLIAM)
	}

	// The schema the migrations ran in, rather than a literal `public`: the same code then grants
	// in production's `public` and in the test suite's per-package schema.
	var schema string
	if err := tx.QueryRowContext(ctx, "SELECT current_schema()").Scan(&schema); err != nil {
		return err
	}
	for _, grant := range appGrants {
		if _, err := tx.ExecContext(ctx, fmt.Sprintf(grant, pgx.Identifier{schema}.Sanitize(), quoted)); err != nil {
			return fmt.Errorf("%s: %w", fmt.Sprintf(grant, schema, role), err)
		}
	}
	return tx.Commit()
}
