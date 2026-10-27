package config_test

import (
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"{{ cookiecutter.go_module }}/internal/config"
)

// Settings behaviour that is easy to regress and expensive to discover in production.
//
// Every case builds its Config with config.FromMap, which ignores the ambient environment. Reading it
// would make these tests pass or fail depending on whose shell runs them — the single most common
// cause of "green locally, red in CI".

func load(t *testing.T, vars map[string]string) config.Config {
	t.Helper()
	cfg, err := config.FromMap(vars)
	if err != nil {
		t.Fatalf("FromMap(%v): %v", vars, err)
	}
	return cfg
}

func refused(t *testing.T, vars map[string]string, mentioning string) {
	t.Helper()
	_, err := config.FromMap(vars)
	if err == nil || !strings.Contains(err.Error(), mentioning) {
		t.Fatalf("FromMap(%v) = %v; want an error mentioning %s", vars, err, mentioning)
	}
}

// deployed is the minimum a deployed environment needs to start.
func deployed(overrides map[string]string) map[string]string {
	vars := map[string]string{"ENVIRONMENT": "production"}
{%- if cookiecutter.use_postgres == "yes" %}
	vars["POSTGRES_PASSWORD"] = "a-real-one"
{%- endif %}
	for key, value := range overrides {
		vars[key] = value
	}
	return vars
}

func TestTheDefaultsAreTheLocalOnes(t *testing.T) {
	cfg := load(t, nil)

	if cfg.Environment != config.Local || cfg.Port != 8000 || cfg.JSONLogs || cfg.GitSHA != "local" {
		t.Fatalf("defaults = %+v", cfg)
	}
	if cfg.IsDeployed() || !cfg.DocsEnabled() {
		t.Fatal("local must be undeployed, with docs on")
	}
	// A service is not reachable from a browser until somebody says which one.
	if len(cfg.CORSOrigins()) != 0 {
		t.Fatalf("CORS origins by default = %v, want none", cfg.CORSOrigins())
	}
}

func TestOnlyStagingAndProductionAreDeployed(t *testing.T) {
	for env, want := range map[string]bool{"local": false, "test": false, "staging": true, "production": true} {
		if got := load(t, deployed(map[string]string{"ENVIRONMENT": env})).IsDeployed(); got != want {
			t.Errorf("IsDeployed(%s) = %v, want %v", env, got, want)
		}
	}
}

// Interactive docs are free reconnaissance in production.
func TestDocsAreOffInProductionOnly(t *testing.T) {
	if load(t, deployed(nil)).DocsEnabled() {
		t.Fatal("docs enabled in production")
	}
	if !load(t, deployed(map[string]string{"ENVIRONMENT": "staging"})).DocsEnabled() {
		t.Fatal("docs disabled in staging")
	}
}

func TestAnUnknownEnvironmentIsRefusedRatherThanGuessed(t *testing.T) {
	refused(t, map[string]string{"ENVIRONMENT": "prod"}, "ENVIRONMENT")
}

func TestAnUnknownLogLevelIsRefusedAndWarningIsAccepted(t *testing.T) {
	refused(t, map[string]string{"LOG_LEVEL": "LOUD"}, "LOG_LEVEL")
	level, err := load(t, map[string]string{"LOG_LEVEL": "warning"}).SlogLevel()
	if err != nil || level != slog.LevelWarn {
		t.Fatalf("SlogLevel(warning) = %v, %v", level, err)
	}
}

func TestOriginsAreParsedFromOneCommaSeparatedString(t *testing.T) {
	cfg := load(t, map[string]string{"CORS_ORIGINS": " http://a.test, https://b.test:8443 ,"})

	if got := fmt.Sprint(cfg.CORSOrigins()); got != "[http://a.test https://b.test:8443]" {
		t.Fatalf("CORSOrigins = %s", got)
	}
}

// Each of these is dead configuration that looks alive, or a wildcard that buys nothing.
func TestAnEntryThatIsNotAnOriginIsRefused(t *testing.T) {
	for _, origin := range []string{"*", "localhost:3000", "http://a.test/", "http://a.test/app", "https://*.example.com", "ftp://a.test"} {
		refused(t, map[string]string{"CORS_ORIGINS": origin}, "CORS_ORIGINS")
	}
}

// A config dump in a log line prints no credential.
func TestSecretsDoNotPrint(t *testing.T) {
	secret := config.Secret("hunter2-but-longer")
	for _, rendered := range []string{fmt.Sprint(secret), fmt.Sprintf("%v %+v %s %#v", secret, secret, secret, secret)} {
		if strings.Contains(rendered, "hunter2") {
			t.Fatalf("a Secret printed its value: %s", rendered)
		}
	}
{%- if cookiecutter.use_postgres == "yes" %}
	cfg := load(t, map[string]string{"JWT_SECRET": "a-signing-key-that-must-never-be-logged"})
	if dump := fmt.Sprintf("%+v %#v", cfg, cfg); strings.Contains(dump, "must-never-be-logged") || strings.Contains(dump, config.PlaceholderSecret) {
		t.Fatalf("a Config dump printed a credential: %s", dump)
	}
{%- endif %}
}
{%- if cookiecutter.use_postgres == "yes" %}

// --- Database ---------------------------------------------------------------

// The point of the validator: a placeholder credential in production *starts*, and a service that
// starts with the wrong credentials fails later and much further from the cause.
func TestThePlaceholderPasswordIsRefusedWhenDeployed(t *testing.T) {
	for _, env := range []string{"staging", "production"} {
		refused(t, map[string]string{"ENVIRONMENT": env, "POSTGRES_PASSWORD": config.PlaceholderSecret}, "POSTGRES_PASSWORD")
	}
}

func TestThePlaceholderPasswordIsFineLocally(t *testing.T) {
	cfg := load(t, map[string]string{"ENVIRONMENT": "test"})

	if cfg.PostgresPassword.Reveal() != config.PlaceholderSecret || !strings.HasPrefix(cfg.DatabaseURL(), "postgres://") {
		t.Fatalf("local database URL = %q", cfg.DatabaseURL())
	}
}

// Cloud SQL hands the service a Unix socket DSN, which cannot be assembled from a host and a port.
func TestTheOverrideWinsOverTheParts(t *testing.T) {
	dsn := "postgres://u:p@/db?host=/cloudsql/project:region:instance"
	cfg := load(t, map[string]string{"ENVIRONMENT": "production", "DATABASE_URL_OVERRIDE": dsn})

	if cfg.DatabaseURL() != dsn {
		t.Fatalf("DatabaseURL = %q, want the override", cfg.DatabaseURL())
	}
}

// A password with URL metacharacters must not be re-parsed as part of the host.
func TestTheAssembledURLEscapesThePassword(t *testing.T) {
	cfg := load(t, map[string]string{"POSTGRES_PASSWORD": "p@ss/w%rd", "POSTGRES_HOST": "db.internal"})

	if !strings.Contains(cfg.DatabaseURL(), "@db.internal:5432/") || strings.Contains(cfg.DatabaseURL(), "p@ss/") {
		t.Fatalf("DatabaseURL = %q", cfg.DatabaseURL())
	}
}

// --- How the process authenticates to the database ----------------------------

// Local development, the suite and CI set nothing new.
func TestPasswordIsTheDefaultDatabaseAuth(t *testing.T) {
	if got := load(t, nil).DatabaseAuth; got != config.AuthPassword {
		t.Fatalf("DATABASE_AUTH defaults to %q, want password", got)
	}
	refused(t, map[string]string{"DATABASE_AUTH": "iam"}, "DATABASE_AUTH")
}

// The AWS migration task is handed the admin login one JSON key per variable, out of RDS's own
// secret. DATABASE_HOST is what switches to those parts — all of them, never a mix with POSTGRES_*.
func TestTheDeployedPartsReplaceTheLocalOnesWhenDatabaseHostIsSet(t *testing.T) {
	cfg := load(t, deployed(map[string]string{
		"POSTGRES_PASSWORD": "",
		"DATABASE_HOST":     "db.abc.eu-west-1.rds.amazonaws.com",
		"DATABASE_NAME":     "app",
		"DATABASE_USER":     "app",
		"DATABASE_PASSWORD": "from-the-master-secret",
	}))
	if want := "postgres://app:from-the-master-secret@db.abc.eu-west-1.rds.amazonaws.com:5432/app"; cfg.DatabaseURL() != want {
		t.Fatalf("DatabaseURL = %q, want %q", cfg.DatabaseURL(), want)
	}
	// And the override still wins over both.
	cfg = load(t, map[string]string{"DATABASE_HOST": "ignored", "DATABASE_NAME": "x", "DATABASE_USER": "x", "DATABASE_URL_OVERRIDE": "postgres://o@h/d"})
	if cfg.DatabaseURL() != "postgres://o@h/d" {
		t.Fatalf("DatabaseURL = %q, want the override", cfg.DatabaseURL())
	}
}

// The GCP migration job reaches Cloud SQL through the socket Cloud Run mounts at /cloudsql.
func TestASocketDirectoryHostBecomesPgxsHostParameter(t *testing.T) {
	cfg := load(t, map[string]string{
		"DATABASE_HOST": "/cloudsql/project:region:instance", "DATABASE_NAME": "app",
		"DATABASE_USER": "postgres", "DATABASE_PASSWORD": "pw",
	})
	if want := "postgres://postgres:pw@:5432/app?host=%2Fcloudsql%2Fproject%3Aregion%3Ainstance"; cfg.DatabaseURL() != want {
		t.Fatalf("DatabaseURL = %q, want %q", cfg.DatabaseURL(), want)
	}
}

func TestTheDeployedPartsAreRequiredTogether(t *testing.T) {
	for _, name := range []string{"DATABASE_NAME", "DATABASE_USER"} {
		vars := map[string]string{"DATABASE_HOST": "db", "DATABASE_NAME": "app", "DATABASE_USER": "app"}
		delete(vars, name)
		refused(t, vars, name)
	}
	// Deployed, the password is required too — empty is no better than the placeholder.
	refused(t, deployed(map[string]string{"DATABASE_HOST": "db", "DATABASE_NAME": "app", "DATABASE_USER": "app"}), "DATABASE_PASSWORD")
}

func rdsIAM(overrides map[string]string) map[string]string {
	vars := deployed(map[string]string{
		"DATABASE_AUTH": "rds-iam",
		"DATABASE_HOST": "db.abc.eu-west-1.rds.amazonaws.com",
		"DATABASE_NAME": "app",
		"DATABASE_USER": "app_iam",
		"AWS_REGION":    "eu-west-1",
	})
	delete(vars, "POSTGRES_PASSWORD")
	for key, value := range overrides {
		vars[key] = value
	}
	return vars
}

func cloudSQLIAM(overrides map[string]string) map[string]string {
	vars := deployed(map[string]string{
		"DATABASE_AUTH":      "cloud-sql-iam",
		"CLOUD_SQL_INSTANCE": "project:europe-west1:service-pg-dev",
		"DATABASE_NAME":      "app",
		"DATABASE_USER":      "service-run-dev@project.iam",
	})
	delete(vars, "POSTGRES_PASSWORD")
	for key, value := range overrides {
		vars[key] = value
	}
	return vars
}

// Deployed with IAM authentication there is no password to refuse: the placeholder check is a
// password-mode check, and the service starts on its identity alone.
func TestEachIAMModeStartsDeployedWithItsSettingsAndNoPassword(t *testing.T) {
	cfg := load(t, rdsIAM(nil))
	if cfg.DatabaseTCPPort() != config.DefaultDatabasePort {
		t.Errorf("DATABASE_PORT defaults to %d", cfg.DatabaseTCPPort())
	}
	load(t, cloudSQLIAM(nil))
}

func TestAnIAMModeIsRefusedWithoutItsSettings(t *testing.T) {
	for _, name := range []string{"DATABASE_HOST", "DATABASE_NAME", "DATABASE_USER", "AWS_REGION"} {
		refused(t, rdsIAM(map[string]string{name: ""}), name)
	}
	for _, name := range []string{"CLOUD_SQL_INSTANCE", "DATABASE_NAME", "DATABASE_USER"} {
		refused(t, cloudSQLIAM(map[string]string{name: ""}), name)
	}
	refused(t, cloudSQLIAM(map[string]string{"CLOUD_SQL_INSTANCE": "just-an-instance"}), "CLOUD_SQL_INSTANCE")
	load(t, cloudSQLIAM(map[string]string{"CLOUD_SQL_INSTANCE": "example.com:project:europe-west1:pg"}))
}

// A password beside IAM authentication is a credential nothing reads, and a stack wired wrong.
func TestAnIAMModeRefusesAPassword(t *testing.T) {
	for _, vars := range []map[string]string{
		rdsIAM(map[string]string{"DATABASE_PASSWORD": "x"}),
		rdsIAM(map[string]string{"DATABASE_URL_OVERRIDE": "postgres://u:p@h/d"}),
		cloudSQLIAM(map[string]string{"DATABASE_PASSWORD": "x"}),
	} {
		refused(t, vars, "the identity is the credential")
	}
}

// The migration job bootstraps the application's role only when told both what it is called and
// how it connects.
func TestTheAppRoleNeedsItsAuthAndTheAuthNeedsItsRole(t *testing.T) {
	refused(t, map[string]string{"DATABASE_APP_ROLE": "app_iam"}, "DATABASE_APP_AUTH")
	refused(t, map[string]string{"DATABASE_APP_ROLE": "app_iam", "DATABASE_APP_AUTH": "password"}, "DATABASE_APP_AUTH")
	refused(t, map[string]string{"DATABASE_APP_AUTH": "rds-iam"}, "DATABASE_APP_ROLE")
	load(t, map[string]string{"DATABASE_APP_ROLE": "app_iam", "DATABASE_APP_AUTH": "rds-iam"})
}

// --- The signing key ----------------------------------------------------------

// A service that starts with the sentinel signs sessions anybody who has read this template can
// forge, and fails silently because everything works.
func TestTheSentinelSigningKeyIsRefusedWhenDeployed(t *testing.T) {
	cfg := load(t, deployed(map[string]string{"JWT_SECRET": config.PlaceholderJWTSecret}))

	if err := cfg.RequireSigningKey(); err == nil || !strings.Contains(err.Error(), "JWT_SECRET") {
		t.Fatalf("RequireSigningKey = %v; want a refusal naming JWT_SECRET", err)
	}
}

// HS256 against a short key is brute-forceable offline from a single captured token.
func TestAShortSigningKeyIsRefusedWhenDeployed(t *testing.T) {
	cfg := load(t, deployed(map[string]string{"JWT_SECRET": strings.Repeat("s", config.MinJWTSecretLength-1)}))

	if err := cfg.RequireSigningKey(); err == nil {
		t.Fatal("a 31-character key was accepted in production")
	}
}

func TestARealSigningKeyStarts(t *testing.T) {
	cfg := load(t, deployed(map[string]string{"JWT_SECRET": strings.Repeat("s", config.MinJWTSecretLength)}))

	if err := cfg.RequireSigningKey(); err != nil {
		t.Fatal(err)
	}
}

// Otherwise `just dev` on a fresh clone needs a secret before it can say hello — and the migrate job,
// which is granted no key because it signs nothing, loads configuration with no key at all.
func TestTheSentinelIsFineLocallyAndLoadingNeverNeedsAKey(t *testing.T) {
	if err := load(t, nil).RequireSigningKey(); err != nil {
		t.Fatal(err)
	}
	// Deployed, with no JWT_SECRET: Load succeeds; only serving refuses.
	if _, err := config.FromMap(deployed(nil)); err != nil {
		t.Fatalf("a deployed migrate job could not load its configuration: %v", err)
	}
}

func TestNonPositiveTokenLifetimesAreRefused(t *testing.T) {
	refused(t, map[string]string{"ACCESS_TOKEN_TTL_SECONDS": "0"}, "ACCESS_TOKEN_TTL_SECONDS")
	refused(t, map[string]string{"REFRESH_TOKEN_TTL_DAYS": "-1"}, "REFRESH_TOKEN_TTL_DAYS")
}

// --- The refresh cookie -------------------------------------------------------

func TestAnUnknownRefreshTransportIsRefused(t *testing.T) {
	refused(t, map[string]string{"REFRESH_TRANSPORT": "header"}, "REFRESH_TRANSPORT")
}

// The CSRF check only accepts listed origins, so the cookie transport with none is a sign-in that
// works and a session that never survives a reload.
func TestTheCookieTransportNeedsAnOrigin(t *testing.T) {
	refused(t, map[string]string{"REFRESH_TRANSPORT": "cookie"}, "CORS_ORIGINS")
	load(t, map[string]string{"REFRESH_TRANSPORT": "cookie", "CORS_ORIGINS": "http://localhost:3000"})
}

// A thirty-day credential over plain HTTP.
func TestAnInsecureCookieIsRefusedWhenDeployed(t *testing.T) {
	refused(t, deployed(map[string]string{"REFRESH_COOKIE_SECURE": "false"}), "REFRESH_COOKIE_SECURE")
	load(t, map[string]string{"REFRESH_COOKIE_SECURE": "false"})
}

// Browsers drop that cookie silently. Refusing it here beats a sign-in that never persists.
func TestSameSiteNoneWithoutSecureIsRefusedEverywhere(t *testing.T) {
	refused(t, map[string]string{"REFRESH_COOKIE_SAMESITE": "none", "REFRESH_COOKIE_SECURE": "false"}, "REFRESH_COOKIE_SAMESITE")
	refused(t, map[string]string{"REFRESH_COOKIE_SAMESITE": "relaxed"}, "REFRESH_COOKIE_SAMESITE")
}
{%- endif %}

// Every problem at once, so a misconfigured deploy is one round of fixes.
func TestEveryProblemIsReportedTogether(t *testing.T) {
	_, err := config.FromMap(map[string]string{"ENVIRONMENT": "prod", "PORT": "70000", "LOG_LEVEL": "LOUD"})
	if err == nil {
		t.Fatal("want an error")
	}
	for _, name := range []string{"ENVIRONMENT", "PORT", "LOG_LEVEL"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error %q does not mention %s", err, name)
		}
	}
}
