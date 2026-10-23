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
	PostgresHost     string `env:"POSTGRES_HOST"     envDefault:"db"`
	PostgresPort     int    `env:"POSTGRES_PORT"     envDefault:"5432"`
	PostgresUser     string `env:"POSTGRES_USER"     envDefault:"{{ cookiecutter.project_slug.replace('-', '_') }}"`
	PostgresPassword Secret `env:"POSTGRES_PASSWORD" envDefault:"local-only-not-a-secret"`
	PostgresDB       string `env:"POSTGRES_DB"       envDefault:"{{ cookiecutter.project_slug.replace('-', '_') }}"`
	// A full pgx DSN; set it and the parts above are ignored. Deployed it is Cloud SQL's Unix
	// socket — `postgres://<user>:<pw>@/<db>?host=/cloudsql/<connection>` — which cannot be
	// assembled from a host and a port.
	DatabaseURLOverride Secret `env:"DATABASE_URL_OVERRIDE"`

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

// DatabaseURL is the pgx DSN: the override when set, otherwise assembled from the parts. Built with
// net/url so a password containing `@`, `/` or `%` is escaped rather than silently re-parsed as
// part of the host.
func (c Config) DatabaseURL() string {
	if c.DatabaseURLOverride != "" {
		return c.DatabaseURLOverride.Reveal()
	}
	dsn := url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(c.PostgresUser, c.PostgresPassword.Reveal()),
		Host:   net.JoinHostPort(c.PostgresHost, strconv.Itoa(c.PostgresPort)),
		Path:   "/" + c.PostgresDB,
	}
	return dsn.String()
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
	// A placeholder credential in production *starts*, and a service that starts with the wrong
	// credentials fails later and much further from the cause. The override carries its own.
	if c.IsDeployed() && c.DatabaseURLOverride == "" && c.PostgresPassword.Reveal() == PlaceholderSecret {
		return []error{errors.New("POSTGRES_PASSWORD must be set outside local and test")}
	}
	return nil
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
