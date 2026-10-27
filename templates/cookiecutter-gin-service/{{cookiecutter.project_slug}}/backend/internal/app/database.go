package app

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"time"

	"cloud.google.com/go/cloudsqlconn"
	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/rds/auth"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"{{ cookiecutter.go_module }}/internal/config"
)

// How long a pooled connection lives, by how it authenticated.
const (
	// passwordConnLifetime recycles a connection before Cloud SQL, RDS or a proxy in between drops
	// it while idle, which would otherwise surface as one failed query per stale connection after
	// every quiet spell.
	passwordConnLifetime = 30 * time.Minute

	// iamConnLifetime is under fifteen minutes on purpose. An IAM credential is checked once, when
	// the connection opens, so a pooled connection outlives its fifteen-minute RDS token without
	// harm — but it also outlives a revocation. With connections recycled, removing the service's
	// rds-db:connect or cloudsql.instanceUser grant locks it out once the credentials it already
	// holds expire, rather than at the next deploy.
	iamConnLifetime = 10 * time.Minute
)

// passwordSource returns the password for one new connection.
type passwordSource func(ctx context.Context) (string, error)

// cloudSQLDial opens a connection to a Cloud SQL instance by its connection name.
type cloudSQLDial func(ctx context.Context, instance string) (net.Conn, error)

// openPool builds the pgx pool for cfg.DatabaseAuth and returns it with the function that
// releases it — the pool, and for `cloud-sql-iam` the connector's dialer behind it.
//
// Lazy: no connection is made until the first query, so the process starts — and /health/live
// answers — while the database is still coming up, and /health/ready says so. A server that refuses
// to start without its database turns a database blip into a crash loop.
//
// Statements use pgx's default, the per-connection prepared-statement cache, which is right for a
// direct connection — including Cloud SQL through the connector, which is the database itself
// rather than a pooler. Behind a transaction-mode pooler (PgBouncer, or Cloud SQL managed
// connection pooling in transaction mode) a prepared statement outlives the server connection it
// was prepared on; set `poolConfig.ConnConfig.DefaultQueryExecMode = pgx.QueryExecModeExec` there.
// Ask Athena for the database access rules.
//
// Pool size is pgx's default, max(4, CPUs). Multiply it by the maximum instance or task count
// before raising it: the product is what the database has to accept, and a small instance accepts
// fewer connections than a busy service scales to.
func openPool(ctx context.Context, cfg config.Config) (*pgxpool.Pool, func(), error) {
	poolConfig, release, err := newPoolConfig(ctx, cfg)
	if err != nil {
		return nil, nil, err
	}
	poolConfig.MaxConnIdleTime = 5 * time.Minute
	poolConfig.HealthCheckPeriod = time.Minute
	pool, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		release()
		return nil, nil, fmt.Errorf("create database pool: %w", err)
	}
	return pool, func() {
		// The pool first: closing the dialer under open connections cuts them mid-query.
		pool.Close()
		release()
	}, nil
}

// newPoolConfig is the pool configuration for the mode, and what to release with the pool.
func newPoolConfig(ctx context.Context, cfg config.Config) (*pgxpool.Config, func(), error) {
	nothing := func() {}
	switch cfg.DatabaseAuth {
	case config.AuthRDSIAM:
		// The default chain: on ECS, the task role's credentials from the container endpoint,
		// cached and refreshed by the SDK. Nothing is fetched until the first token is signed.
		awsConfig, err := awsconfig.LoadDefaultConfig(ctx, awsconfig.WithRegion(cfg.AWSRegion))
		if err != nil {
			return nil, nil, fmt.Errorf("load AWS configuration: %w", err)
		}
		poolConfig, err := rdsIAMPoolConfig(cfg, awsConfig.Credentials)
		return poolConfig, nothing, err
	case config.AuthCloudSQLIAM:
		// Lazy refresh, because Cloud Run throttles the CPU outside a request: the connector's
		// background certificate refresh would stall, and the next connection would find its
		// certificate expired. Lazily, the refresh happens on the connection that needs it.
		dialer, err := cloudsqlconn.NewDialer(ctx, cloudsqlconn.WithIAMAuthN(), cloudsqlconn.WithLazyRefresh())
		if err != nil {
			return nil, nil, fmt.Errorf("create the Cloud SQL connector: %w", err)
		}
		poolConfig, err := cloudSQLPoolConfig(cfg, func(ctx context.Context, instance string) (net.Conn, error) {
			return dialer.Dial(ctx, instance)
		})
		release := func() { _ = dialer.Close() }
		if err != nil {
			release()
			return nil, nil, err
		}
		return poolConfig, release, nil
	default:
		poolConfig, err := pgxpool.ParseConfig(cfg.DatabaseURL())
		if err != nil {
			// Never wrap the parse error itself: it can quote the DSN, password included.
			return nil, nil, errors.New("DATABASE_URL_OVERRIDE / DATABASE_* / POSTGRES_* do not form a valid connection string")
		}
		poolConfig.MaxConnLifetime = passwordConnLifetime
		return poolConfig, nothing, nil
	}
}

// rdsIAMPoolConfig connects to RDS as cfg.DatabaseUser with a token for a password, signed with
// creds on every new connection.
//
// sslmode=require because RDS refuses an IAM login over a plaintext connection — and the parameter
// group refuses plaintext anyway. Not verify-full: that needs the RDS CA bundle in the image's
// trust store, and the connection never leaves the VPC.
func rdsIAMPoolConfig(cfg config.Config, creds aws.CredentialsProvider) (*pgxpool.Config, error) {
	endpoint := net.JoinHostPort(cfg.DatabaseHost, strconv.Itoa(cfg.DatabaseTCPPort()))
	dsn := url.URL{
		Scheme:   "postgres",
		User:     url.User(cfg.DatabaseUser),
		Host:     endpoint,
		Path:     "/" + cfg.DatabaseName,
		RawQuery: "sslmode=require",
	}
	poolConfig, err := pgxpool.ParseConfig(dsn.String())
	if err != nil {
		return nil, fmt.Errorf("DATABASE_HOST / DATABASE_PORT / DATABASE_NAME / DATABASE_USER: %w", err)
	}
	// Signing is local — no network call — so a token per connection costs microseconds, and a
	// token is never older than the connection it opened.
	withPerConnectionPassword(poolConfig, func(ctx context.Context) (string, error) {
		return auth.BuildAuthToken(ctx, endpoint, cfg.AWSRegion, cfg.DatabaseUser, creds)
	})
	poolConfig.MaxConnLifetime = iamConnLifetime
	return poolConfig, nil
}

// cloudSQLPoolConfig connects to Cloud SQL as cfg.DatabaseUser through dial, which is the
// connector: it opens the TLS tunnel and presents the service account's identity, so the DSN has
// no password, no host to resolve and no TLS of its own.
func cloudSQLPoolConfig(cfg config.Config, dial cloudSQLDial) (*pgxpool.Config, error) {
	dsn := url.URL{
		Scheme: "postgres",
		User:   url.User(cfg.DatabaseUser),
		// A name for pgx's bookkeeping only. The lookup and the dial below never resolve it.
		Host:     "cloud-sql-connector",
		Path:     "/" + cfg.DatabaseName,
		RawQuery: "sslmode=disable",
	}
	poolConfig, err := pgxpool.ParseConfig(dsn.String())
	if err != nil {
		return nil, fmt.Errorf("DATABASE_NAME / DATABASE_USER: %w", err)
	}
	instance := cfg.CloudSQLInstance
	poolConfig.ConnConfig.LookupFunc = func(_ context.Context, host string) ([]string, error) {
		return []string{host}, nil
	}
	poolConfig.ConnConfig.DialFunc = func(ctx context.Context, _, _ string) (net.Conn, error) {
		return dial(ctx, instance)
	}
	poolConfig.MaxConnLifetime = iamConnLifetime
	return poolConfig, nil
}

// withPerConnectionPassword asks source for the password each time the pool opens a connection.
// Once per connection, not once per pool: an IAM token expires in fifteen minutes, a pool lives as
// long as the process, and a token minted at start-up would lock the service out of every
// connection it opens after that.
func withPerConnectionPassword(poolConfig *pgxpool.Config, source passwordSource) {
	poolConfig.BeforeConnect = func(ctx context.Context, connConfig *pgx.ConnConfig) error {
		password, err := source(ctx)
		if err != nil {
			return fmt.Errorf("database auth token: %w", err)
		}
		connConfig.Password = password
		return nil
	}
}
