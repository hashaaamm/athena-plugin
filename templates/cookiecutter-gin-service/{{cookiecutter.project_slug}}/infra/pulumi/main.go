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
		Frontend:      cfg.FrontendEnabled,
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

	// What the backend service runs with. Separate from runtimeEnv because the migration job
	// shares that map and takes plain strings, while this one may carry a stack output.
	backendEnv := map[string]pulumi.StringInput{}
	for name, value := range runtimeEnv {
		backendEnv[name] = pulumi.String(value)
	}

	// A browser client, served by nginx from its own Cloud Run service. Declared before the
	// backend on purpose: the backend's CORS allowlist is this service's origin, and an origin
	// does not exist until Cloud Run has assigned one.
	var frontend *components.Service
	if cfg.FrontendEnabled {
		frontend, err = components.NewService(ctx, cfg, components.ServiceArgs{
			App:          cfg.Slug + "-web",
			RuntimeEmail: identities.WebRuntime.Email,
			// nginx-unprivileged binds $PORT, which the frontend's production image defaults to
			// 8080. Same number as the backend and no conflict: each service gets its own URL.
			Port: 8080,
			// `/` rather than /health/live. A static bundle has no dependency it could be
			// un-ready about, so it ships no health endpoint and needs none — nginx answers `/`
			// from disk the moment it is listening, which is exactly what the probe is asking.
			HealthPath: "/",
			// Deliberately no environment, no secrets, no database, no jobs. Vite inlines every
			// VITE_* value into the bundle at BUILD time, so a variable set on this revision
			// arrives after the bundle exists and changes nothing. The API origin is a Docker
			// build argument — see frontend-cd.yml.
			//
			// Small and highly concurrent, because serving a file holds nothing per request.
			Memory:      "256Mi",
			Concurrency: 80,
			// Tight, unlike the backend's. nginx is listening in under a second, so a broken
			// revision should report in half a minute rather than after a hundred seconds.
			StartupFailureThreshold: 6,
		}, afterApis...)
		if err != nil {
			return nil, err
		}
		// Without this the deployed SPA loads, makes its first request and is refused by the
		// browser, which reads as "the API is down". The value cannot be typed in ahead of time:
		// it is the frontend service's URL, and Cloud Run assigns it.
		backendEnv["CORS_ORIGINS"] = frontend.URL
{%- if cookiecutter.use_postgres == "yes" %}
		// A browser keeps the refresh token in an HttpOnly cookie rather than in JavaScript-
		// readable storage. Read infra/README.md before relying on it across two run.app URLs:
		// they are two sites, and a SameSite=Strict cookie does not cross between them.
		backendEnv["REFRESH_TRANSPORT"] = pulumi.String("cookie")
{%- endif %}
	}

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
		"cloud_run_frontend_service": pulumi.String(""),
		"frontend_url":               pulumi.String(""),
		"cloud_run_migrate_job":      pulumi.String(""),
		"cloud_sql_instance":         pulumi.String(""),
	}
	if frontend != nil {
		exports["cloud_run_frontend_service"] = frontend.Service.Name
		// Read by nothing in frontend-cd.yml — that pipeline asks Cloud Run for the URL it just
		// deployed to. Exported and synced anyway, because it is the address a human, a status
		// page or an end-to-end suite needs.
		exports["frontend_url"] = frontend.URL
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
