// Package config is the only place in the process that reads the environment.
//
// `os.Getenv` does not appear anywhere else in application code, and the linter enforces it
// (forbidigo and depguard in .golangci.yml): a stray read is invisible to validation, so a typo in a
// variable name becomes a default nobody chose, discovered in production. Everything the service
// can be configured with is a field on Config, parsed and validated once at start-up.
//
// Ask Athena for the configuration and secrets-management rules.
package config

import (
	"errors"
	"fmt"
	"log/slog"
{%- if cookiecutter.use_postgres == "yes" %}
	"net"
{%- endif %}
	"net/url"
	"slices"
{%- if cookiecutter.use_postgres == "yes" %}
	"strconv"
{%- endif %}
	"strings"

	"github.com/caarlos0/env/v11"
)

// Environment is where the process is running. It decides which safety checks apply.
type Environment string

// The four environments. Anything else is refused at start-up rather than treated as one of them.
const (
	Local      Environment = "local"
	Test       Environment = "test"
	Staging    Environment = "staging"
	Production Environment = "production"
)
{%- if cookiecutter.use_postgres == "yes" %}

// PlaceholderSecret is the local database password. A sentinel, not a credential: Validate refuses
// to start with it outside local and test, because a placeholder password in production is worse
// than a missing one — it starts.
const PlaceholderSecret = "local-only-not-a-secret"

// MinJWTSecretLength is the shortest signing key a deployed server accepts. HS256 signs with the
// configured secret directly, so the secret *is* the key; RFC 7518 §3.2 puts the floor at the
// hash's own output size, 32 bytes for SHA-256. A shorter key is brute-forceable offline from one
// captured token, and captured tokens are the normal case.
const MinJWTSecretLength = 32

// PlaceholderJWTSecret is the signing equivalent of PlaceholderSecret, and long enough to clear the
// floor above on purpose, so local tokens are minted exactly as deployed ones are. It is still a
// sentinel: RequireSigningKey refuses to serve with it anywhere deployed.
const PlaceholderJWTSecret = "local-only-not-a-real-signing-key"

// Refresh-token transports. Where the refresh token travels is a property of the client, not of
// the session: a server-to-server caller keeps it in the JSON body, a browser must keep it where
// script cannot read it — an httpOnly cookie.
const (
	TransportBody   = "body"
	TransportCookie = "cookie"
)

// How a process authenticates to Postgres: DATABASE_AUTH. One value per way a credential can
// reach a connection, and the deployed service never uses the first.
const (
	// AuthPassword is a password in the DSN: local development, the test suite, CI, and the
	// deployed migration job, which connects as the admin login.
	AuthPassword = "password"
	// AuthRDSIAM is RDS IAM database authentication: the password on every new connection is a
	// token signed with the task's own AWS identity, valid for fifteen minutes. Nothing stores it.
	AuthRDSIAM = "rds-iam"
	// AuthCloudSQLIAM is Cloud SQL IAM database authentication through the Cloud SQL Go connector,
	// which carries the service account's identity in the client certificate. No password exists.
	AuthCloudSQLIAM = "cloud-sql-iam"
)

// DatabaseAuths is every value DATABASE_AUTH accepts.
var DatabaseAuths = []string{AuthPassword, AuthRDSIAM, AuthCloudSQLIAM}

// DefaultDatabasePort is DATABASE_PORT when it is unset.
const DefaultDatabasePort = 5432
{%- endif %}

// Secret is a string that does not print. fmt, %v, %+v, %#v and slog all render it as
// "[redacted]", so dumping a Config into a log line — the most common way a credential reaches a
// log aggregator — prints nothing worth stealing. Call Reveal at the one place that needs the value.
type Secret string

// Reveal returns the value. Grep for it: every call is a place a credential leaves this type.
func (s Secret) Reveal() string { return string(s) }

func (s Secret) String() string { return "[redacted]" }

// GoString covers %#v, which would otherwise bypass String.
func (s Secret) GoString() string { return `"[redacted]"` }

// LogValue covers slog, which does not call String on its own.
func (s Secret) LogValue() slog.Value { return slog.StringValue("[redacted]") }

// Config is the whole of the service's configuration, typed. The `envDefault` tags are the local
// defaults; a deployed value never comes from a file — see .env.example for where each one lives.
type Config struct {
	// --- Application ----------------------------------------------------
	Environment Environment `env:"ENVIRONMENT" envDefault:"local"`
	// Cloud Run assigns the port and the container MUST listen on it. The production image sets
	// 8080; compose and the React dev tooling expect 8000.
	Port     int    `env:"PORT"      envDefault:"8000"`
	LogLevel string `env:"LOG_LEVEL" envDefault:"INFO"`
	// Human-readable locally; JSON wherever a log aggregator parses lines into fields.
	JSONLogs bool `env:"JSON_LOGS" envDefault:"false"`
	// The immutable build SHA. The image bakes it in; /health/ready reports it, and Sentry needs it
	// to attribute a regression to a commit.
	GitSHA string `env:"GIT_SHA" envDefault:"local"`

	// --- Browser client -------------------------------------------------
	// Comma-separated origins a browser client is served from, e.g. "http://localhost:3000".
	// Empty — the default — means no cross-origin access at all, which is right for a service with
	// no browser client. There is deliberately no "allow everything": a wildcard origin cannot carry
	// credentials anyway, so it buys nothing but a finding in a pen test.
	CORSOriginsRaw string `env:"CORS_ORIGINS"`
{%- if cookiecutter.use_postgres == "yes" %}

	// --- Database -------------------------------------------------------
	// How this process authenticates to Postgres: `password`, `rds-iam` or `cloud-sql-iam` (the
	// Auth constants). `password` is the default so that local development, the test suite and CI
	// need nothing new; a deployed service is set to one of the IAM modes by its stack, and holds
	// no database password at all.
	DatabaseAuth string `env:"DATABASE_AUTH" envDefault:"password"`

	// The local parts, in `password` mode only. Compose hands the same POSTGRES_* values to the db
	// container, which is why they keep the names the Postgres image reads.
	PostgresHost     string `env:"POSTGRES_HOST"     envDefault:"db"`
	PostgresPort     int    `env:"POSTGRES_PORT"     envDefault:"5432"`
	PostgresUser     string `env:"POSTGRES_USER"     envDefault:"{{ cookiecutter.project_slug.replace('-', '_') }}"`
	PostgresPassword Secret `env:"POSTGRES_PASSWORD" envDefault:"local-only-not-a-secret"`
	PostgresDB       string `env:"POSTGRES_DB"       envDefault:"{{ cookiecutter.project_slug.replace('-', '_') }}"`

	// The deployed parts. In `password` mode, setting DATABASE_HOST switches from the POSTGRES_*
	// parts to these — all of them, never a mix — which is how the AWS migration task is handed
	// the admin login out of RDS's own secret, one JSON key per variable. In the IAM modes they
	// are the connection itself, minus the password nobody has.
	//
	// A DATABASE_HOST that starts with `/` is a Unix socket directory: Cloud SQL's, at
	// /cloudsql/<connection-name>, for the GCP migration job.
	DatabaseHost     string `env:"DATABASE_HOST"`
	DatabasePort     int    `env:"DATABASE_PORT"` // DefaultDatabasePort when unset
	DatabaseName     string `env:"DATABASE_NAME"`
	DatabaseUser     string `env:"DATABASE_USER"`
	DatabasePassword Secret `env:"DATABASE_PASSWORD"`
	// A full pgx DSN, in `password` mode only; set it and every part above is ignored.
	DatabaseURLOverride Secret `env:"DATABASE_URL_OVERRIDE"`

	// `rds-iam`: the region the token is signed for. The credentials come from the AWS SDK's
	// default chain — on ECS, the task role.
	AWSRegion string `env:"AWS_REGION"`
	// `cloud-sql-iam`: the instance, as `project:region:instance`. The connector dials it; there is
	// no host, no port and no socket.
	CloudSQLInstance string `env:"CLOUD_SQL_INSTANCE"`

	// The application's database identity, which `migrate up` creates (on RDS) and grants on every
	// run, after the migrations, so a table a migration just added is covered. Empty — the default
	// — skips the step: locally the one login is both. DATABASE_APP_AUTH is how that identity
	// connects, `rds-iam` or `cloud-sql-iam`, and decides what the bootstrap does.
	DatabaseAppRole string `env:"DATABASE_APP_ROLE"`
	DatabaseAppAuth string `env:"DATABASE_APP_AUTH"`

	// --- Authentication -------------------------------------------------
	// The HS256 signing key. There is no usable default: RequireSigningKey refuses the sentinel, and
	// anything too short, everywhere except local and test. A template that shipped a working
	// default would ship every service generated from it the same forgeable key.
	JWTSecret Secret `env:"JWT_SECRET" envDefault:"local-only-not-a-real-signing-key"`
	// `iss` and `aud`, verified on every decode. They are what stops a token minted by staging, or by
	// another service that happens to share the key, from being accepted here.
	JWTIssuer   string `env:"JWT_ISSUER"   envDefault:"{{ cookiecutter.project_slug }}"`
	JWTAudience string `env:"JWT_AUDIENCE" envDefault:"{{ cookiecutter.project_slug }}"`
	// Fifteen minutes. Nothing checks an access token against the database, so this number is also
	// the revocation latency: revoking a session stops new access tokens at once, and the one
	// already issued keeps working until it expires. Deactivating a user is the same sentence.
	AccessTokenTTLSeconds int `env:"ACCESS_TOKEN_TTL_SECONDS" envDefault:"900"`
	// How long a refresh token, and the refresh_sessions row behind it, stays usable. Every refresh
	// replaces it, so this is the longest a client can go without calling before it must sign in
	// again — not how long a session can last.
	RefreshTokenTTLDays int `env:"REFRESH_TOKEN_TTL_DAYS" envDefault:"30"`
	// "body" or "cookie". See the Transport constants, and internal/http/handler/auth_cookie.go.
	RefreshTransport string `env:"REFRESH_TRANSPORT" envDefault:"body"`
	// The refresh cookie's Secure attribute. Refused as false anywhere deployed: without it the
	// browser sends a thirty-day credential over plain HTTP. Local development serves the API on
	// http://localhost, which is the only reason it can be turned off.
	RefreshCookieSecure bool `env:"REFRESH_COOKIE_SECURE" envDefault:"true"`
	// The refresh cookie's SameSite attribute. `strict` keeps it off every request a third-party page
	// starts, and needs the SPA and the API on the same *site* — app.example.com and api.example.com
	// are; two *.run.app hostnames are not, because run.app is a public suffix. `none` is the
	// cross-site setting, and then only the CSRF check stands between the cookie and a forged request.
	RefreshCookieSameSite string `env:"REFRESH_COOKIE_SAMESITE" envDefault:"strict"`
{%- endif %}
{%- if cookiecutter.use_sentry == "yes" %}

	// --- Observability --------------------------------------------------
	// Empty means no error tracking, and that MUST be the local default: developer machines
	// reporting into a real project make environment filtering useless.
	SentryDSN              Secret  `env:"SENTRY_DSN"`
	SentryTracesSampleRate float64 `env:"SENTRY_TRACES_SAMPLE_RATE" envDefault:"0.1"`
{%- endif %}
}

// Load reads the process environment, applies the defaults and validates the result. It is the
// only caller of the environment in the service.
func Load() (Config, error) {
	return parse(env.Options{})
}

// FromMap builds a Config from the given variables and nothing else. The ambient environment is
// ignored, so a test passes or fails on what it says rather than on whose shell runs it.
func FromMap(vars map[string]string) (Config, error) {
	if vars == nil {
		vars = map[string]string{}
	}
	return parse(env.Options{Environment: vars})
}

func parse(opts env.Options) (Config, error) {
	cfg, err := env.ParseAsWithOptions[Config](opts)
	if err != nil {
		return Config{}, fmt.Errorf("parse configuration: %w", err)
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// IsDeployed reports whether this is an environment real users reach.
func (c Config) IsDeployed() bool {
	return c.Environment == Staging || c.Environment == Production
}

// DocsEnabled reports whether /docs and /openapi.json are served. Interactive docs are a local and
// staging affordance; in production they are free reconnaissance unless the API is meant to be public.
func (c Config) DocsEnabled() bool {
	return c.Environment != Production
}

// CORSOrigins is CORS_ORIGINS parsed, trimmed and with empty entries dropped, so the variable can
// be a plain comma-separated string and no caller has to remember to strip whitespace.
func (c Config) CORSOrigins() []string {
	var origins []string
	for _, origin := range strings.Split(c.CORSOriginsRaw, ",") {
		if origin = strings.TrimSpace(origin); origin != "" {
			origins = append(origins, origin)
		}
	}
	return origins
}

// SlogLevel is LOG_LEVEL as a slog level. WARNING is accepted beside WARN, because that is the name
// every other runtime in the estate uses and a level that silently falls back to INFO is a lie.
func (c Config) SlogLevel() (slog.Level, error) {
	switch strings.ToUpper(strings.TrimSpace(c.LogLevel)) {
	case "DEBUG":
		return slog.LevelDebug, nil
	case "INFO":
		return slog.LevelInfo, nil
	case "WARN", "WARNING":
		return slog.LevelWarn, nil
	case "ERROR":
		return slog.LevelError, nil
	default:
		return slog.LevelInfo, fmt.Errorf("LOG_LEVEL must be DEBUG, INFO, WARNING or ERROR, not %q", c.LogLevel)
	}
}
{%- if cookiecutter.use_postgres == "yes" %}

// DatabaseURL is the `password`-mode pgx DSN. The precedence, first match wins:
//
//  1. DATABASE_URL_OVERRIDE, verbatim;
//  2. the DATABASE_* parts, when DATABASE_HOST is set;
//  3. the POSTGRES_* parts — local development, the suite and CI.
//
// Built with net/url so a password containing `@`, `/` or `%` is escaped rather than silently
// re-parsed as part of the host. The IAM modes have no password to put in a DSN; internal/app
// builds their connection from the parts.
func (c Config) DatabaseURL() string {
	if c.DatabaseURLOverride != "" {
		return c.DatabaseURLOverride.Reveal()
	}
	if c.DatabaseHost == "" {
		return pgURL(c.PostgresHost, c.PostgresPort, c.PostgresDB, url.UserPassword(c.PostgresUser, c.PostgresPassword.Reveal()))
	}
	return pgURL(c.DatabaseHost, c.DatabaseTCPPort(), c.DatabaseName, url.UserPassword(c.DatabaseUser, c.DatabasePassword.Reveal()))
}

// DatabaseTCPPort is DATABASE_PORT, or DefaultDatabasePort when it is unset.
func (c Config) DatabaseTCPPort() int {
	if c.DatabasePort == 0 {
		return DefaultDatabasePort
	}
	return c.DatabasePort
}

// pgURL is a pgx DSN for a host and port, or for a Unix socket directory — a host starting with
// `/`, which pgx takes from the `host` query parameter because a path is not a URL authority.
func pgURL(host string, port int, database string, user *url.Userinfo) string {
	dsn := url.URL{Scheme: "postgres", User: user, Path: "/" + database}
	if strings.HasPrefix(host, "/") {
		// The port still matters: it names the socket file, .s.PGSQL.<port>.
		dsn.Host = net.JoinHostPort("", strconv.Itoa(port))
		dsn.RawQuery = url.Values{"host": {host}}.Encode()
	} else {
		dsn.Host = net.JoinHostPort(host, strconv.Itoa(port))
	}
	return dsn.String()
}

// databasePassword is the password DatabaseURL assembles, for the deployed-placeholder check.
func (c Config) databasePassword() string {
	if c.DatabaseHost == "" {
		return c.PostgresPassword.Reveal()
	}
	return c.DatabasePassword.Reveal()
}

// RequireSigningKey refuses to serve HTTP with a signing key anybody could guess. Called when the
// router is built (internal/app), so it runs in every process that can issue a token.
//
// Not part of Validate, on purpose. Only a credential *every* process needs belongs there, and the
// migration job is granted no signing key because it signs nothing — checking at load would turn
// least privilege into a start-up crash. A deployed server that started with the sentinel would
// issue sessions anybody who has read this template can forge, and it would fail silently, because
// everything works.
func (c Config) RequireSigningKey() error {
	if !c.IsDeployed() {
		return nil
	}
	secret := c.JWTSecret.Reveal()
	if secret == PlaceholderJWTSecret || len(secret) < MinJWTSecretLength {
		return fmt.Errorf("JWT_SECRET must be set outside local and test, and must be at least %d characters", MinJWTSecretLength)
	}
	return nil
}
{%- endif %}

// Validate fails at start-up rather than at first use, and reports every problem at once so a
// misconfigured deploy is one round of fixes rather than one per variable.
func (c Config) Validate() error {
	var problems []error
	if !slices.Contains([]Environment{Local, Test, Staging, Production}, c.Environment) {
		problems = append(problems, fmt.Errorf("ENVIRONMENT must be local, test, staging or production, not %q", c.Environment))
	}
	if c.Port < 1 || c.Port > 65535 {
		problems = append(problems, fmt.Errorf("PORT must be between 1 and 65535, not %d", c.Port))
	}
	if _, err := c.SlogLevel(); err != nil {
		problems = append(problems, err)
	}
	for _, origin := range c.CORSOrigins() {
		if err := validateOrigin(origin); err != nil {
			problems = append(problems, err)
		}
	}
{%- if cookiecutter.use_postgres == "yes" %}
	problems = append(problems, c.validateDatabase()...)
	problems = append(problems, c.validateAuth()...)
{%- endif %}
{%- if cookiecutter.use_sentry == "yes" %}
	if c.SentryTracesSampleRate < 0 || c.SentryTracesSampleRate > 1 {
		problems = append(problems, fmt.Errorf("SENTRY_TRACES_SAMPLE_RATE must be between 0 and 1, not %v", c.SentryTracesSampleRate))
	}
{%- endif %}
	return errors.Join(problems...)
}
{%- if cookiecutter.use_postgres == "yes" %}

func (c Config) validateDatabase() []error {
	var problems []error
	missing := func(pairs ...[2]string) {
		for _, pair := range pairs {
			if strings.TrimSpace(pair[1]) == "" {
				problems = append(problems, fmt.Errorf("DATABASE_AUTH=%s requires %s", c.DatabaseAuth, pair[0]))
			}
		}
	}
	// A credential beside IAM authentication is one nothing reads — and the sign of a stack that
	// is wired to the wrong mode, which is better refused at start-up than discovered as a login
	// that works for the wrong reason.
	noPassword := func() {
		if c.DatabaseURLOverride != "" || c.DatabasePassword != "" {
			problems = append(problems, fmt.Errorf(
				"DATABASE_AUTH=%s takes no DATABASE_PASSWORD or DATABASE_URL_OVERRIDE: the identity is the credential", c.DatabaseAuth))
		}
	}
	switch c.DatabaseAuth {
	case AuthPassword:
		if c.DatabaseURLOverride == "" && c.DatabaseHost != "" {
			missing([2]string{"DATABASE_NAME", c.DatabaseName}, [2]string{"DATABASE_USER", c.DatabaseUser})
		}
		// A placeholder credential in production *starts*, and a service that starts with the
		// wrong credentials fails later and much further from the cause. The override carries
		// its own.
		if password := c.databasePassword(); c.IsDeployed() && c.DatabaseURLOverride == "" &&
			(password == "" || password == PlaceholderSecret) {
			problems = append(problems, errors.New(
				"DATABASE_PASSWORD (POSTGRES_PASSWORD when DATABASE_HOST is unset) must be set outside local and test"))
		}
	case AuthRDSIAM:
		missing(
			[2]string{"DATABASE_HOST", c.DatabaseHost}, [2]string{"DATABASE_NAME", c.DatabaseName},
			[2]string{"DATABASE_USER", c.DatabaseUser}, [2]string{"AWS_REGION", c.AWSRegion},
		)
		noPassword()
	case AuthCloudSQLIAM:
		missing(
			[2]string{"CLOUD_SQL_INSTANCE", c.CloudSQLInstance}, [2]string{"DATABASE_NAME", c.DatabaseName},
			[2]string{"DATABASE_USER", c.DatabaseUser},
		)
		// project:region:instance, where a domain-scoped project id carries a colon of its own.
		if parts := strings.Split(c.CloudSQLInstance, ":"); c.CloudSQLInstance != "" &&
			(len(parts) < 3 || slices.Contains(parts, "")) {
			problems = append(problems, fmt.Errorf(
				"CLOUD_SQL_INSTANCE %q is not a connection name such as project:region:instance", c.CloudSQLInstance))
		}
		noPassword()
	default:
		problems = append(problems, fmt.Errorf("DATABASE_AUTH must be one of %v, not %q", DatabaseAuths, c.DatabaseAuth))
	}
	if c.DatabasePort < 0 || c.DatabasePort > 65535 {
		problems = append(problems, fmt.Errorf("DATABASE_PORT must be between 1 and 65535, not %d", c.DatabasePort))
	}
	// The bootstrap needs both or neither: a role with no way to connect is a role nothing uses,
	// and a way to connect with no role is a grant to nobody.
	switch {
	case c.DatabaseAppRole == "" && c.DatabaseAppAuth != "":
		problems = append(problems, errors.New("DATABASE_APP_AUTH requires DATABASE_APP_ROLE"))
	case c.DatabaseAppRole != "" && c.DatabaseAppAuth != AuthRDSIAM && c.DatabaseAppAuth != AuthCloudSQLIAM:
		problems = append(problems, fmt.Errorf(
			"DATABASE_APP_ROLE requires DATABASE_APP_AUTH to be %s or %s, not %q", AuthRDSIAM, AuthCloudSQLIAM, c.DatabaseAppAuth))
	}
	return problems
}

func (c Config) validateAuth() []error {
	var problems []error
	if c.AccessTokenTTLSeconds <= 0 {
		problems = append(problems, fmt.Errorf("ACCESS_TOKEN_TTL_SECONDS must be positive, not %d", c.AccessTokenTTLSeconds))
	}
	if c.RefreshTokenTTLDays <= 0 {
		problems = append(problems, fmt.Errorf("REFRESH_TOKEN_TTL_DAYS must be positive, not %d", c.RefreshTokenTTLDays))
	}
	switch c.RefreshTransport {
	case TransportBody:
	case TransportCookie:
		// Every browser sends Origin on a cross-origin POST, and on a same-origin one too, so the
		// CSRF check in front of the cookie routes refuses every refresh unless the client's origin
		// is listed. Starting anyway would be a sign-in that works and a session that never survives
		// a reload, which is a long afternoon to diagnose.
		if len(c.CORSOrigins()) == 0 {
			problems = append(problems, errors.New("REFRESH_TRANSPORT=cookie requires CORS_ORIGINS: the CSRF check only accepts listed origins"))
		}
	default:
		problems = append(problems, fmt.Errorf("REFRESH_TRANSPORT must be body or cookie, not %q", c.RefreshTransport))
	}
	if !slices.Contains([]string{"strict", "lax", "none"}, c.RefreshCookieSameSite) {
		problems = append(problems, fmt.Errorf("REFRESH_COOKIE_SAMESITE must be strict, lax or none, not %q", c.RefreshCookieSameSite))
	}
	// Browsers drop SameSite=None without Secure silently, so that pair is refused everywhere
	// rather than shipped as a sign-in that mysteriously never persists.
	if c.RefreshCookieSameSite == "none" && !c.RefreshCookieSecure {
		problems = append(problems, errors.New("REFRESH_COOKIE_SAMESITE=none requires REFRESH_COOKIE_SECURE=true"))
	}
	if c.IsDeployed() && !c.RefreshCookieSecure {
		problems = append(problems, errors.New("REFRESH_COOKIE_SECURE must be true outside local and test"))
	}
	return problems
}
{%- endif %}

// validateOrigin accepts exactly what a browser sends in an Origin header: a scheme, a host and an
// optional port, nothing else. A trailing slash or a path never matches a real Origin, so the
// entry would be dead configuration that looks alive; `*` is refused for the reason on Config.
func validateOrigin(origin string) error {
	parsed, err := url.Parse(origin)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
		parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil ||
		strings.Contains(origin, "*") {
		return fmt.Errorf("CORS_ORIGINS entry %q must be an origin such as https://app.example.com", origin)
	}
	return nil
}
