// Command pulumi is the stack's composition root.
//
// deploy reads top to bottom as a dependency graph: APIs first because everything needs them, then
// the things with no dependencies, then the services that tie them together.
//
// App-specific configuration lives here rather than inside internal/components/service.go. The
// component describes "a Cloud Run service in this project"; what makes this one what it is — a
// port, a probe path, a database, a migration job — is an argument.
//
// Exports are named for what consumes them. Each one a pipeline reads becomes a GitHub repository
// variable: infra/scripts/sync-github.sh reads them straight out of `pulumi stack output`, and the
// names on the right of its mapping must match the `vars.` names in .github/workflows/cd.yml
// exactly. A stack exporting `workload_identity_provider` to a pipeline expecting WIF_PROVIDER is a
// deploy that never runs and never says why, which is why contract_test.go checks all three.
package main

import (
	"maps"
	"slices"
	"strings"

	"github.com/pulumi/pulumi-gcp/sdk/v10/go/gcp"
	"github.com/pulumi/pulumi-gcp/sdk/v10/go/gcp/organizations"
{%- if cookiecutter.use_postgres == "yes" %}
	"github.com/pulumi/pulumi-random/sdk/v4/go/random"
{%- endif %}
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"{{ cookiecutter.go_module }}/infra/pulumi/internal/components"
	"{{ cookiecutter.go_module }}/infra/pulumi/internal/stackconfig"
)

func main() {
	pulumi.Run(func(ctx *pulumi.Context) error {
		cfg, err := stackconfig.Load(ctx)
		if err != nil {
			return err
		}
		exports, err := deploy(ctx, cfg)
		if err != nil {
			return err
		}
		for _, name := range slices.Sorted(maps.Keys(exports)) {
			ctx.Export(name, exports[name])
		}
		return nil
	})
}

// Exports is every stack output, by name. Every name is exported in every configuration — empty
// when the thing it names does not exist — so a consumer can tell "no migration job" from "nobody
// ran sync-github.sh".
type Exports map[string]pulumi.StringInput

// deploy builds the whole stack from a validated configuration and returns its outputs.
func deploy(ctx *pulumi.Context, cfg stackconfig.StackConfig) (Exports, error) {
	// One explicit provider. Pulumi.yaml disables the default GCP provider, because a default one
	// would pick up ambient gcloud configuration and make two machines plan differently.
	provider, err := gcp.NewProvider(ctx, "gcp", &gcp.ProviderArgs{
		Project: pulumi.String(cfg.Project),
		Region:  pulumi.String(cfg.Region),
	})
	if err != nil {
		return nil, err
	}
	opts := []pulumi.ResourceOption{pulumi.Provider(provider)}

	project := organizations.LookupProjectOutput(ctx, organizations.LookupProjectOutputArgs{
		ProjectId: pulumi.String(cfg.Project),
	}, pulumi.Provider(provider))

	apis, err := components.NewApis(ctx, cfg, opts...)
	if err != nil {
		return nil, err
	}
	// Everything below needs the APIs enabled first, or the first `up` fails halfway through.
	afterApis := with(opts, pulumi.DependsOn(apis.Services))

	registry, err := components.NewRegistry(ctx, cfg, afterApis...)
	if err != nil {
		return nil, err
	}
	identities, err := components.NewIdentities(ctx, cfg, components.IdentitiesArgs{
		ProjectNumber: project.Number(),
	}, afterApis...)
	if err != nil {
		return nil, err
	}

	secretsArgs := components.SecretsArgs{AccessorEmail: identities.Runtime.Email}
{%- if cookiecutter.use_postgres == "yes" %}

	database, err := components.NewDatabase(ctx, cfg, afterApis...)
	if err != nil {
		return nil, err
	}

	// The HS256 key the service signs access tokens with. Generated, never chosen: a key a human
	// typed is a key a human can reuse, paste into a ticket, or make twelve characters long. 48
	// alphanumerics is well past the 32-character floor `serve` enforces at start-up. It travels
	// from Secret Manager into the service's environment and nowhere else — in particular, the
	// migration job below is not granted it, because it signs nothing.
	jwtSecret, err := random.NewRandomPassword(ctx, "jwt-secret", &random.RandomPasswordArgs{
		Length:  pulumi.Int(48),
		Special: pulumi.Bool(false),
	}, afterApis...)
	if err != nil {
		return nil, err
	}
	secretsArgs.Generated = map[string]pulumi.StringInput{
		"database-url": database.DatabaseURL,
		"jwt-secret":   jwtSecret.Result,
	}
{%- endif %}
{%- if cookiecutter.use_sentry == "yes" %}
	secretsArgs.ManualVersions = map[string]string{"sentry-dsn": cfg.SentryDSNVersion}
{%- endif %}

	secrets, err := components.NewSecrets(ctx, cfg, secretsArgs, afterApis...)
	if err != nil {
		return nil, err
	}

	// The deployed environment: values the application reads and a human may need to reason
	// about. Anything credential-shaped is a secret mount instead.
	//
	// ENVIRONMENT is the APPLICATION's environment and not this stack's, so it comes from
	// appEnvironment in Pulumi.<stack>.yaml rather than from a literal here. A literal is how a
	// stack called `dev` deploys a service that believes it is production, serves no /docs, and
	// says nothing on the page about why. stackconfig has the distinction in full.
	//
	// No PORT: the production image sets 8080 and Cloud Run injects PORT anyway. No worker count
	// either — a Go binary is one process using every core it is given.
	runtimeEnv := map[string]string{
		"ENVIRONMENT": cfg.AppEnvironment,
		"LOG_LEVEL":   "INFO",
		// JSON everywhere a log aggregator reads it.
		"JSON_LOGS": "true",
	}

	// Environment variable name -> the logical secret it is mounted from.
	secretEnv := map[string]string{}
{%- if cookiecutter.use_postgres == "yes" %}
	secretEnv["DATABASE_URL_OVERRIDE"] = "database-url"
	secretEnv["JWT_SECRET"] = "jwt-secret"
{%- endif %}
{%- if cookiecutter.use_sentry == "yes" %}
	// Mounted only once its version number is in stack configuration, because an empty container
	// fails the deploy outright. Add the value, then set sentryDsnVersion.
	if cfg.SentryDSNVersion != "" {
		secretEnv["SENTRY_DSN"] = "sentry-dsn"
	}
{%- endif %}

	// What the backend service runs with: runtimeEnv plus the HTTP-facing settings. Separate from
	// runtimeEnv because the migration job shares that map and serves no HTTP — and because the
	// application refuses REFRESH_TRANSPORT=cookie without CORS_ORIGINS at load, handing the job
	// one without the other would stop it booting.
	backendEnv := map[string]pulumi.StringInput{}
	for name, value := range runtimeEnv {
		backendEnv[name] = pulumi.String(value)
	}
	// A browser client is its own repository and its own stack (ADR-0022): `corsOrigins` is its
	// `site_url`. Without it the deployed SPA makes its first request and the browser refuses the
	// answer, which reads as "the API is down". Unset when empty, so the default is no CORS at all.
	if len(cfg.CORSOrigins) > 0 {
		backendEnv["CORS_ORIGINS"] = pulumi.String(strings.Join(cfg.CORSOrigins, ","))
	}
{%- if cookiecutter.use_postgres == "yes" %}
	// Always set, so the deployed value is the one in Pulumi.<stack>.yaml rather than whatever the
	// image defaults to. `cookie` for a browser client; read infra/README.md before relying on it
	// across two default hostnames — they are two sites, and a SameSite=Strict cookie does not
	// cross between them.
	backendEnv["REFRESH_TRANSPORT"] = pulumi.String(cfg.RefreshTransport)
{%- endif %}

	backendArgs := components.ServiceArgs{
		App:          cfg.Slug,
		RuntimeEmail: identities.Runtime.Email,
		// The production image sets PORT=8080 and the binary binds it. Cloud Run may override
		// PORT, and the binary reads it from the environment rather than hard-coding one.
		Port: 8080,
		// Liveness, not readiness. Readiness may legitimately be false on a first deploy.
		HealthPath: "/health/live",
		Env:        backendEnv,
		SecretEnv:  secretEnv,
		SecretRefs: secrets.Refs,
	}
{%- if cookiecutter.use_postgres == "yes" %}
	backendArgs.ConnectionName = database.ConnectionName
	// The same image as the service, a different command. The binary embeds its migrations, so
	// the job needs the database and nothing else — no signing key, no Sentry DSN.
	migrate := components.JobSpec{
		Name:      "migrate",
		Command:   []string{"/app/api"},
		Args:      []string{"migrate", "up"},
		Env:       runtimeEnv,
		SecretEnv: map[string]string{"DATABASE_URL_OVERRIDE": "database-url"},
	}
	backendArgs.Jobs = []components.JobSpec{migrate}
{%- endif %}

	// Depends on the secret VERSIONS, not just the containers: a service that waits only for the
	// container races the version into existence and fails with "version was not found".
	backend, err := components.NewService(ctx, cfg, backendArgs, with(afterApis, pulumi.DependsOn(secrets.Versions))...)
	if err != nil {
		return nil, err
	}

	// --- Outputs. These are the CD pipeline's configuration. ---------------------------------
	exports := Exports{
		"gcp_project":       pulumi.String(cfg.Project),
		"gcp_region":        pulumi.String(cfg.Region),
		"image_repo":        registry.URL,
		"wif_provider":      identities.ProviderName,
		"deploy_sa":         identities.CI.Email,
		"runtime_sa":        identities.Runtime.Email,
		"cloud_run_service": backend.Service.Name,
		"service_url":       backend.URL,
		// Empty unless the thing they name exists. sync-github.sh deletes the repository
		// variable for an empty output rather than setting it, and every workflow step that
		// uses one is gated on it — so turning a feature off un-configures its pipeline too,
		// instead of leaving it pointed at a resource that no longer exists.
		"cloud_run_migrate_job": pulumi.String(""),
		"cloud_sql_instance":    pulumi.String(""),
	}
{%- if cookiecutter.use_postgres == "yes" %}
	exports["cloud_run_migrate_job"] = backend.Jobs["migrate"].Name
	exports["cloud_sql_instance"] = database.ConnectionName
{%- endif %}

	return exports, nil
}

// with returns opts plus more, without writing into opts' backing array — two slices built from
// one base must not share a tail.
func with(opts []pulumi.ResourceOption, more ...pulumi.ResourceOption) []pulumi.ResourceOption {
	return append(slices.Clone(opts), more...)
}
