package app_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"

	"{{ cookiecutter.go_module }}/internal/app"
	"{{ cookiecutter.go_module }}/internal/config"
	"{{ cookiecutter.go_module }}/internal/testdb"
	"{{ cookiecutter.go_module }}/migrations"
)

// How the service reaches its database in each DATABASE_AUTH mode. The suite runs against a real
// Postgres in `password` mode; the IAM modes are exercised up to the network — the token a
// connection would present, the instance it would dial — with a fake credential in place of a cloud.

// testPoolConfig is the suite's own database, with the password removed from the DSN so that a
// credential source is the only way a connection can get one.
func testPoolConfig(t *testing.T) (*pgxpool.Config, string) {
	t.Helper()
	poolConfig, err := pgxpool.ParseConfig(testdb.Config(t).DatabaseURL())
	if err != nil {
		t.Fatal(err)
	}
	password := poolConfig.ConnConfig.Password
	poolConfig.ConnConfig.Password = ""
	return poolConfig, password
}

// The point of BeforeConnect over a password in the DSN: an IAM token expires in fifteen minutes and
// the pool lives as long as the process, so every new connection must ask for a token of its own. A
// connection the pool reuses is not asked again — RDS checks the token at connect only.
func TestEveryNewConnectionAsksForItsOwnPassword(t *testing.T) {
	poolConfig, password := testPoolConfig(t)
	var calls atomic.Int32
	app.WithPerConnectionPassword(poolConfig, func(context.Context) (string, error) {
		calls.Add(1)
		return password, nil
	})
	poolConfig.MaxConns = 3
	pool, err := pgxpool.NewWithConfig(context.Background(), poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	// Three connections held at once is three connects.
	held := make([]*pgxpool.Conn, 0, 3)
	for range 3 {
		conn, err := pool.Acquire(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, conn)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("the credential source was asked %d times for 3 connections", got)
	}
	for _, conn := range held {
		conn.Release()
	}
	// Reused, not reopened: still three.
	var wg sync.WaitGroup
	for range 3 {
		wg.Go(func() {
			if _, err := pool.Exec(context.Background(), "SELECT 1"); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if got := calls.Load(); got != 3 {
		t.Fatalf("the credential source was asked %d times; a pooled connection was re-authenticated", got)
	}
}

// A credential source that fails fails the connection, with the reason — not a login attempt with
// an empty password that the server rejects for a reason nobody can trace.
func TestAFailingCredentialSourceFailsTheConnection(t *testing.T) {
	poolConfig, _ := testPoolConfig(t)
	app.WithPerConnectionPassword(poolConfig, func(context.Context) (string, error) {
		return "", errors.New("no credentials in the chain")
	})
	pool, err := pgxpool.NewWithConfig(context.Background(), poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(context.Background()); err == nil || !strings.Contains(err.Error(), "no credentials in the chain") {
		t.Fatalf("Ping = %v; want the credential source's error", err)
	}
}

// Readiness is the same query whichever way the connection authenticated.
func TestReadinessAnswersThroughAPerConnectionCredential(t *testing.T) {
	poolConfig, password := testPoolConfig(t)
	app.WithPerConnectionPassword(poolConfig, func(context.Context) (string, error) { return password, nil })
	pool, err := pgxpool.NewWithConfig(context.Background(), poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)

	ready := newAPIOn(t, testConfig(t, nil), pool).call(http.MethodGet, "/health/ready", nil, nil)

	expectStatus(t, ready, http.StatusOK)
	if body := object(t, ready); body["database"] != "ok" {
		t.Fatalf("readiness %v", body)
	}
}

func iamConfig(t *testing.T, vars map[string]string) config.Config {
	t.Helper()
	vars["ENVIRONMENT"] = "production"
	cfg, err := config.FromMap(vars)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// The token is signed for the endpoint, region and user RDS will check it against, over TLS, and
// minted when a connection opens rather than when the pool is built. Signing is local, so this
// needs a credential and no network.
func TestRDSIAMSignsATokenPerConnectionOverTLS(t *testing.T) {
	cfg := iamConfig(t, map[string]string{
		"DATABASE_AUTH": "rds-iam", "DATABASE_HOST": "db.abc.eu-west-1.rds.amazonaws.com",
		"DATABASE_NAME": "app", "DATABASE_USER": "app_iam", "AWS_REGION": "eu-west-1",
	})
	var retrieved atomic.Int32
	creds := aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		retrieved.Add(1)
		return aws.Credentials{AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "not-a-real-key", Source: "test"}, nil
	})
	poolConfig, err := app.RDSIAMPoolConfig(cfg, creds)
	if err != nil {
		t.Fatal(err)
	}
	conn := poolConfig.ConnConfig
	if conn.Host != cfg.DatabaseHost || conn.Port != 5432 || conn.User != "app_iam" || conn.Database != "app" || conn.Password != "" {
		t.Fatalf("connection = %s:%d %s/%s, password set: %v", conn.Host, conn.Port, conn.User, conn.Database, conn.Password != "")
	}
	// sslmode=require: TLS, and no plaintext fallback to send a token over.
	if conn.TLSConfig == nil || len(conn.Fallbacks) != 0 {
		t.Fatalf("TLS = %v, fallbacks = %d; want TLS only", conn.TLSConfig != nil, len(conn.Fallbacks))
	}
	if poolConfig.MaxConnLifetime <= 0 || poolConfig.MaxConnLifetime >= 15*time.Minute {
		t.Fatalf("MaxConnLifetime = %v; want under the token's fifteen minutes", poolConfig.MaxConnLifetime)
	}
	if retrieved.Load() != 0 {
		t.Fatal("a credential was used before any connection opened")
	}

	for range 2 {
		connConfig := conn.Copy()
		if err := poolConfig.BeforeConnect(context.Background(), connConfig); err != nil {
			t.Fatal(err)
		}
		token := connConfig.Password
		if !strings.HasPrefix(token, "db.abc.eu-west-1.rds.amazonaws.com:5432?") ||
			!strings.Contains(token, "Action=connect") || !strings.Contains(token, "DBUser=app_iam") ||
			!strings.Contains(token, "X-Amz-Expires=900") || !strings.Contains(token, "eu-west-1%2Frds-db%2Faws4_request") {
			t.Fatalf("token %q is not an RDS IAM token for app_iam in eu-west-1", token)
		}
	}
	if got := retrieved.Load(); got != 2 {
		t.Fatalf("two connections signed with %d credential retrievals", got)
	}
}

// The connector is the transport: the pool dials the instance through it, by connection name, with
// no password and no TLS of its own — and never resolves a hostname, because there is none.
func TestCloudSQLIAMDialsTheInstanceThroughTheConnector(t *testing.T) {
	cfg := iamConfig(t, map[string]string{
		"DATABASE_AUTH": "cloud-sql-iam", "CLOUD_SQL_INSTANCE": "project:europe-west1:service-pg-dev",
		"DATABASE_NAME": "app", "DATABASE_USER": "service-run-dev@project.iam",
	})
	var (
		mu     sync.Mutex
		dialed []string
	)
	refused := errors.New("the fake connector refuses")
	poolConfig, err := app.CloudSQLPoolConfig(cfg, func(_ context.Context, instance string) (net.Conn, error) {
		mu.Lock()
		defer mu.Unlock()
		dialed = append(dialed, instance)
		return nil, refused
	})
	if err != nil {
		t.Fatal(err)
	}
	conn := poolConfig.ConnConfig
	if conn.User != "service-run-dev@project.iam" || conn.Database != "app" || conn.Password != "" || conn.TLSConfig != nil {
		t.Fatalf("connection = %s/%s, password set: %v, TLS: %v", conn.User, conn.Database, conn.Password != "", conn.TLSConfig != nil)
	}
	if poolConfig.MaxConnLifetime <= 0 || poolConfig.MaxConnLifetime >= 15*time.Minute {
		t.Fatalf("MaxConnLifetime = %v", poolConfig.MaxConnLifetime)
	}
	if hosts, err := conn.LookupFunc(context.Background(), conn.Host); err != nil || len(hosts) != 1 || hosts[0] != conn.Host {
		t.Fatalf("LookupFunc(%q) = %v, %v; want no resolution at all", conn.Host, hosts, err)
	}

	pool, err := pgxpool.NewWithConfig(context.Background(), poolConfig)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	if err := pool.Ping(context.Background()); !errors.Is(err, refused) {
		t.Fatalf("Ping = %v; want the connector's own error", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(dialed) == 0 || dialed[0] != cfg.CloudSQLInstance {
		t.Fatalf("dialed %v, want %s", dialed, cfg.CloudSQLInstance)
	}
}

// --- The migrator bootstraps the application's role ---------------------------------------------

func randomRole(t *testing.T, prefix string) string {
	t.Helper()
	suffix := make([]byte, 4)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	return prefix + hex.EncodeToString(suffix)
}

// migratedSchema is a fresh schema with every migration applied, as the migrator leaves it, and a
// database/sql handle on it. Roles are cluster-wide, so each test's role is dropped when it ends.
func migratedSchema(t *testing.T) (*pgxpool.Pool, string) {
	t.Helper()
	pool, schema := testdb.PoolOnNewSchema(t)
	db := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = db.Close() })
	provider, err := migrations.NewProvider(db)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Up(context.Background()); err != nil {
		t.Fatal(err)
	}
	return pool, schema
}

func dropRole(t *testing.T, pool *pgxpool.Pool, role string) {
	t.Helper()
	t.Cleanup(func() {
		quoted := pgx.Identifier{role}.Sanitize()
		_, _ = pool.Exec(context.Background(), "DROP OWNED BY "+quoted)
		_, _ = pool.Exec(context.Background(), "DROP ROLE IF EXISTS "+quoted)
	})
}

func privilege(t *testing.T, pool *pgxpool.Pool, query string, args ...any) bool {
	t.Helper()
	var held bool
	if err := pool.QueryRow(context.Background(), query, args...).Scan(&held); err != nil {
		t.Fatal(err)
	}
	return held
}

// Data privileges, on every table — including one a later migration adds — and nothing that
// changes the schema. Run twice, because it runs on every deploy.
func TestTheMigratorCreatesTheRDSAppRoleAndGrantsItDataOnly(t *testing.T) {
	pool, schema := migratedSchema(t)
	ctx := context.Background()
	// RDS has the rds_iam role; a plain Postgres does not, so the test makes one.
	if _, err := pool.Exec(ctx, `DO $$ BEGIN CREATE ROLE rds_iam; EXCEPTION WHEN duplicate_object THEN NULL; END $$`); err != nil {
		t.Fatal(err)
	}
	role := randomRole(t, "app_iam_")
	dropRole(t, pool, role)
	db := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = db.Close() })

	for range 2 {
		if err := app.BootstrapAppRole(ctx, db, role, config.AuthRDSIAM); err != nil {
			t.Fatal(err)
		}
	}

	if !privilege(t, pool, "SELECT rolcanlogin FROM pg_roles WHERE rolname = $1", role) {
		t.Error("the app role cannot log in")
	}
	if !privilege(t, pool, "SELECT pg_has_role($1, 'rds_iam', 'MEMBER')", role) {
		t.Error("the app role is not in rds_iam, so RDS would ask it for a password")
	}
	users := schema + ".users"
	for _, want := range []string{"SELECT", "INSERT", "UPDATE", "DELETE"} {
		if !privilege(t, pool, "SELECT has_table_privilege($1, $2, $3)", role, users, want) {
			t.Errorf("the app role lacks %s on users", want)
		}
	}
	for _, denied := range []string{"TRUNCATE", "REFERENCES", "TRIGGER"} {
		if privilege(t, pool, "SELECT has_table_privilege($1, $2, $3)", role, users, denied) {
			t.Errorf("the app role holds %s on users", denied)
		}
	}
	if privilege(t, pool, "SELECT has_schema_privilege($1, $2, 'CREATE')", role, schema) {
		t.Error("the app role may create objects in the schema")
	}

	// A table the next migration creates, as the migrator: the default privileges cover it.
	if _, err := pool.Exec(ctx, "CREATE TABLE "+pgx.Identifier{schema, "later"}.Sanitize()+" (id bigint GENERATED ALWAYS AS IDENTITY)"); err != nil {
		t.Fatal(err)
	}
	if !privilege(t, pool, "SELECT has_table_privilege($1, $2, 'INSERT')", role, schema+".later") {
		t.Error("a table created after the bootstrap is not writable by the app role")
	}
	if !privilege(t, pool, "SELECT has_sequence_privilege($1, $2, 'USAGE')", role, schema+".later_id_seq") {
		t.Error("a sequence created after the bootstrap is not usable by the app role")
	}
}

// On Cloud SQL the stack creates the IAM user; the migrator grants to it and never creates it, or
// the stack could no longer create the real one.
func TestTheMigratorGrantsTheCloudSQLUserButNeverCreatesIt(t *testing.T) {
	pool, schema := migratedSchema(t)
	ctx := context.Background()
	db := stdlib.OpenDBFromPool(pool)
	t.Cleanup(func() { _ = db.Close() })
	role := randomRole(t, "service-run-dev-") + "@project.iam"
	dropRole(t, pool, role)

	err := app.BootstrapAppRole(ctx, db, role, config.AuthCloudSQLIAM)
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("BootstrapAppRole on a missing role = %v; want a refusal", err)
	}
	if privilege(t, pool, "SELECT EXISTS (SELECT 1 FROM pg_roles WHERE rolname = $1)", role) {
		t.Fatal("the migrator created the Cloud SQL IAM user itself")
	}

	// What Cloud SQL does for a CLOUD_IAM_SERVICE_ACCOUNT user, closely enough for the grants.
	if _, err := pool.Exec(ctx, "CREATE ROLE "+pgx.Identifier{role}.Sanitize()+" WITH LOGIN"); err != nil {
		t.Fatal(err)
	}
	if err := app.BootstrapAppRole(ctx, db, role, config.AuthCloudSQLIAM); err != nil {
		t.Fatal(err)
	}
	if !privilege(t, pool, "SELECT has_table_privilege($1, $2, 'UPDATE')", role, schema+".refresh_sessions") {
		t.Error("the Cloud SQL user cannot write refresh_sessions")
	}
}
