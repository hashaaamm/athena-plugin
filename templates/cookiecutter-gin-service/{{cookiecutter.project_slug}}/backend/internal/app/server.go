package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"time"
{% if cookiecutter.use_postgres == "yes" %}
	"github.com/jackc/pgx/v5/pgxpool"
{% endif %}
	"{{ cookiecutter.go_module }}/internal/config"
{%- if cookiecutter.use_sentry == "yes" %}
	"{{ cookiecutter.go_module }}/internal/observability"
{%- endif %}
)

// shutdownTimeout is how long in-flight requests get to finish after SIGTERM. Cloud Run allows ten
// seconds between SIGTERM and SIGKILL; this leaves room for closing the pool and flushing Sentry
// inside them. Removing the drain turns every deploy into a source of 502s that no test shows you.
const shutdownTimeout = 8 * time.Second

// Serve runs the HTTP server until ctx is cancelled — by SIGTERM, in main — then drains it.
func Serve(ctx context.Context, cfg config.Config, logger *slog.Logger) error {
{%- if cookiecutter.use_sentry == "yes" %}
	flush, err := observability.Init(cfg)
	if err != nil {
		return err
	}
	defer flush()
{%- endif %}
{%- if cookiecutter.use_postgres == "yes" %}

	pool, err := openPool(ctx, cfg)
	if err != nil {
		return err
	}
	// Release connections on shutdown, or every redeploy leaks them until Postgres refuses new ones.
	defer pool.Close()

	router, err := NewRouter(cfg, Deps{Logger: logger, DB: pool})
{%- else %}

	router, err := NewRouter(cfg, Deps{Logger: logger})
{%- endif %}
	if err != nil {
		return err
	}

	server := &http.Server{
		// Every interface, because the container's network namespace is the boundary. PORT is read
		// at run time: Cloud Run assigns it, and the container MUST listen on it.
		Addr:    net.JoinHostPort("", strconv.Itoa(cfg.Port)),
		Handler: router,
		// Each timeout is a resource a slow or hostile client could otherwise hold for ever. The
		// header timeout is the Slowloris one, and Go's default for it is "no limit".
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
		ErrorLog:          slog.NewLogLogger(logger.Handler(), slog.LevelWarn),
	}

	serveErr := make(chan error, 1)
	go func() {
		logger.InfoContext(ctx, "startup", "environment", cfg.Environment, "version", cfg.GitSHA, "port", cfg.Port)
		serveErr <- server.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		// The listener failed — a port in use, most often. Nothing to drain.
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
	}

	// Stop accepting, let in-flight requests finish, then return so the deferred closes run.
	// WithoutCancel: ctx is already cancelled, and the drain needs a deadline of its own.
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()
	logger.InfoContext(shutdownCtx, "shutdown")
	if err := server.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("drain: %w", err)
	}
	return nil
}
{%- if cookiecutter.use_postgres == "yes" %}

// openPool builds the pgx pool. Lazy: no connection is made until the first query, so the process
// starts — and /health/live answers — while the database is still coming up, and /health/ready says
// so. A server that refuses to start without its database turns a database blip into a crash loop.
//
// Statements use pgx's default, the per-connection prepared-statement cache, which is right for a
// direct connection — including Cloud SQL's Unix socket, which is the database itself rather than a
// pooler. Behind a transaction-mode pooler (PgBouncer, or Cloud SQL managed connection pooling in
// transaction mode) a prepared statement outlives the server connection it was prepared on; set
// `poolConfig.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeExec` there. Ask Athena for the
// database access rules.
//
// Pool size is pgx's default, max(4, CPUs). Multiply it by Cloud Run's maximum instance count before
// raising it: the product is what the database has to accept, and a small Cloud SQL tier accepts
// fewer connections than a busy service scales to.
func openPool(ctx context.Context, cfg config.Config) (*pgxpool.Pool, error) {
	poolConfig, err := pgxpool.ParseConfig(cfg.DatabaseURL())
	if err != nil {
		// Never wrap the parse error itself: it can quote the DSN, password included.
		return nil, errors.New("DATABASE_URL_OVERRIDE / POSTGRES_* do not form a valid connection string")
	}
	// Recycled before Cloud SQL or a proxy in between drops an idle connection under us, which would
	// otherwise surface as one failed query per stale connection after every quiet spell.
	poolConfig.MaxConnIdleTime = 5 * time.Minute
	poolConfig.MaxConnLifetime = 30 * time.Minute
	poolConfig.HealthCheckPeriod = time.Minute
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("create database pool: %w", err)
	}
	return pool, nil
}
{%- endif %}

// Healthcheck is the container's HEALTHCHECK: GET /health/live on this container's own port. A
// subcommand of the binary because the production image is distroless — no shell, no curl — and
// that absence is a security property worth more than a familiar command.
func Healthcheck(ctx context.Context, cfg config.Config) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	url := "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(cfg.Port)) + "/health/live"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return fmt.Errorf("healthcheck: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("healthcheck: %s answered %d", url, response.StatusCode)
	}
	return nil
}
