// Command pulumi is the stack's composition root.
//
// deploy reads top to bottom as a dependency graph: the provider first because everything goes
// through it, then the things with no dependencies — network, registry, logs, cluster — then the
// data and the identities that read it, then the services that tie them together, and last the
// deploy role's grant, which names the services.
//
// App-specific configuration lives here rather than inside internal/components/service.go. The
// component describes "an Express service in this stack"; what makes this one what it is — a
// port, a probe path, a database, a migration task — is an argument.
//
// Exports are named for what consumes them. Each one a pipeline reads becomes a GitHub repository
// variable: infra/scripts/sync-github.sh reads them straight out of `pulumi stack output`, and the
// names on the right of its mapping must match the `vars.` names in .github/workflows/cd.yml
// exactly. A stack exporting `deploy_role` to a pipeline expecting AWS_DEPLOY_ROLE_ARN is a deploy
// that never runs and never says why, which is why contract_test.go checks all three.
package main

import (
	"maps"
	"slices"
	"strconv"
	"strings"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws"
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
// when the thing it names does not exist — so a consumer can tell "no migration task" from "nobody
// ran sync-github.sh".
type Exports map[string]pulumi.StringInput

// deploy builds the whole stack from a validated configuration and returns its outputs.
func deploy(ctx *pulumi.Context, cfg stackconfig.StackConfig) (Exports, error) {
	// One explicit provider. Pulumi.yaml disables the default AWS provider, because a default one
	// would pick up whatever profile and region the shell happens to have and make two machines
	// plan differently.
	//
	// allowedAccountIds is the wrong-account guard: credentials for any other account fail before
	// the provider reads or plans a single resource, rather than half way through creating this
	// stack somewhere it was never meant to be. The default tags are on every resource that takes
	// tags, so the console and the bill can both answer "what is this, and which stack owns it".
	provider, err := aws.NewProvider(ctx, "aws", &aws.ProviderArgs{
		Region:            pulumi.String(cfg.Region),
		AllowedAccountIds: pulumi.StringArray{pulumi.String(cfg.AccountID)},
		DefaultTags: &aws.ProviderDefaultTagsArgs{
			Tags: pulumi.StringMap{
				"app":          pulumi.String(cfg.Slug),
				"environment":  pulumi.String(cfg.Environment),
				"managed-by":   pulumi.String("pulumi"),
				"pulumi-stack": pulumi.String(ctx.Stack()),
			},
		},
	})
	if err != nil {
		return nil, err
	}
	opts := []pulumi.ResourceOption{pulumi.Provider(provider)}

	network, err := components.NewNetwork(ctx, cfg, opts...)
	if err != nil {
		return nil, err
	}
	registry, err := components.NewRegistry(ctx, cfg, opts...)
	if err != nil {
		return nil, err
	}
	logs, err := components.NewLogs(ctx, cfg, components.LogsArgs{
		Migrate: {% if cookiecutter.use_postgres == "yes" %}true{% else %}false{% endif %},
	}, opts...)
	if err != nil {
		return nil, err
	}
	cluster, err := components.NewCluster(ctx, cfg, opts...)
	if err != nil {
		return nil, err
	}

	// Every secret the stack creates is empty, and mounted only once its VersionId is in stack
	// configuration — a secret with no value is a task that cannot start.
	secretsArgs := components.SecretsArgs{Versions: map[string]string{}}
{%- if cookiecutter.use_postgres == "yes" %}
	secretsArgs.Versions["jwt-secret"] = cfg.JWTSecretVersion

	database, err := components.NewDatabase(ctx, cfg, components.DatabaseArgs{
		SubnetIDs:       network.IsolatedSubnetIDs,
		SecurityGroupID: network.DBSecurityGroup.ID(),
	}, opts...)
	if err != nil {
		return nil, err
	}
{%- endif %}
{%- if cookiecutter.use_sentry == "yes" %}
	secretsArgs.Versions["sentry-dsn"] = cfg.SentryDSNVersion
{%- endif %}

	secrets, err := components.NewSecrets(ctx, cfg, secretsArgs, opts...)
	if err != nil {
		return nil, err
	}

	secretARNs := make([]pulumi.StringOutput, 0, len(secrets.ARNs))
	for _, name := range slices.Sorted(maps.Keys(secrets.ARNs)) {
		secretARNs = append(secretARNs, secrets.ARNs[name])
	}
	identitiesArgs := components.IdentitiesArgs{SecretARNs: secretARNs}
{%- if cookiecutter.use_postgres == "yes" %}
	identitiesArgs.MasterSecretARN = database.MasterSecretARN
	identitiesArgs.DatabaseResourceID = database.ResourceID
	identitiesArgs.DatabaseAppRole = cfg.DatabaseAppRole
{%- endif %}
	identities, err := components.NewIdentities(ctx, cfg, identitiesArgs, opts...)
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
	// No PORT: the production image sets 8080, and Express Mode routes to the container port
	// declared below. No worker count either — a Go binary is one process using every core it is
	// given.
	runtimeEnv := map[string]pulumi.StringInput{
		"ENVIRONMENT": pulumi.String(cfg.AppEnvironment),
		"LOG_LEVEL":   pulumi.String("INFO"),
		// JSON everywhere a log aggregator reads it.
		"JSON_LOGS": pulumi.String("true"),
	}

	// Environment variable name -> the logical secret it is mounted from.
	secretEnv := map[string]string{}
{%- if cookiecutter.use_postgres == "yes" %}
	// Without it the service refuses to start, which is why secrets_ready gates CD on it. The
	// first `up` runs the bootstrap image, which needs no key.
	if cfg.JWTSecretVersion != "" {
		secretEnv["JWT_SECRET"] = "jwt-secret"
	}
{%- endif %}
{%- if cookiecutter.use_sentry == "yes" %}
	// Add the value, then set sentryDsnVersion.
	if cfg.SentryDSNVersion != "" {
		secretEnv["SENTRY_DSN"] = "sentry-dsn"
	}
{%- endif %}

	// What the backend service runs with: runtimeEnv plus the HTTP-facing settings and its own way
	// into the database. Separate from runtimeEnv because the migration task shares that map, serves
	// no HTTP, and connects differently — and because the application refuses
	// REFRESH_TRANSPORT=cookie without CORS_ORIGINS at load, handing the task one without the other
	// would stop it booting.
	backendEnv := maps.Clone(runtimeEnv)
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

	// The service logs in as databaseAppRole with a token its task role signs per connection: no
	// password, no secret. AWS_REGION is the region the token is signed for.
	backendEnv["DATABASE_AUTH"] = pulumi.String("rds-iam")
	backendEnv["DATABASE_HOST"] = database.Address
	backendEnv["DATABASE_PORT"] = pulumi.String(strconv.Itoa(components.PostgresPort))
	backendEnv["DATABASE_NAME"] = pulumi.String(components.DBName)
	backendEnv["DATABASE_USER"] = pulumi.String(cfg.DatabaseAppRole)
	backendEnv["AWS_REGION"] = pulumi.String(cfg.Region)
{%- endif %}

	// Every task in this stack runs in the public subnets, as the app security group — see
	// components.Network for why both.
	subnets := network.PublicSubnetIDs
	securityGroups := pulumi.StringArray{network.AppSecurityGroup.ID()}

	// Depends on the roles' grants, for the DRAINING race in Identities.
	backend, err := components.NewService(ctx, cfg, components.ServiceArgs{
		App:                   cfg.Slug,
		Cluster:               cluster.Name,
		ExecutionRoleARN:      identities.Execution.Arn,
		InfrastructureRoleARN: identities.Infrastructure.Arn,
		TaskRoleARN:           identities.Runtime.Arn,
		// The production image sets PORT=8080 and the binary binds it.
		Port: 8080,
		// Liveness, not readiness. Readiness may legitimately be false on a first deploy, and a
		// load balancer that never sees a healthy target never finishes the deployment.
		HealthPath:     "/health/live",
		Env:            backendEnv,
		SecretEnv:      secretEnv,
		SecretRefs:     secrets.Refs,
		LogGroup:       logs.Backend.Name,
		Subnets:        subnets,
		SecurityGroups: securityGroups,
	}, with(opts, pulumi.DependsOn(identities.ServiceDependencies))...)
	if err != nil {
		return nil, err
	}

	deployAccess := components.DeployAccessArgs{
		Role:           identities.Deploy,
		RepositoryARNs: []pulumi.StringOutput{registry.Backend.Arn},
		ServiceARNs:    []pulumi.StringOutput{backend.ARN},
		Cluster:        cluster.Name,
		PassRoleARNs: []pulumi.StringOutput{
			identities.Execution.Arn, identities.Runtime.Arn, identities.Infrastructure.Arn,
		},
	}
{%- if cookiecutter.use_postgres == "yes" %}

	// The same image as the service, a different command, and a different way in. The binary
	// embeds its migrations, so the task needs the database and nothing else — no signing key, no
	// Sentry DSN, no task role — and it is the one thing that holds a database password.
	//
	// It logs in as the master user, from the secret RDS manages, over TLS: PGSSLMODE is libpq's
	// own variable, which pgx reads beneath the DSN, so the client refuses plaintext as well as the
	// server. Then, after the migrations, it creates databaseAppRole if it is missing and grants
	// it row privileges.
	migrateEnv := maps.Clone(runtimeEnv)
	migrateEnv["DATABASE_AUTH"] = pulumi.String("password")
	migrateEnv["DATABASE_HOST"] = database.Address
	migrateEnv["DATABASE_PORT"] = pulumi.String(strconv.Itoa(components.PostgresPort))
	migrateEnv["DATABASE_NAME"] = pulumi.String(components.DBName)
	migrateEnv["PGSSLMODE"] = pulumi.String("require")
	migrateEnv["DATABASE_APP_ROLE"] = pulumi.String(cfg.DatabaseAppRole)
	migrateEnv["DATABASE_APP_AUTH"] = pulumi.String("rds-iam")
	migrations, err := components.NewMigrations(ctx, cfg, components.MigrationsArgs{
		ExecutionRoleARN: identities.MigrateExecution.Arn,
		EntryPoint:       []string{"/app/api"},
		Command:          []string{"migrate", "up"},
		Env:              migrateEnv,
		SecretEnv: map[string]string{
			"DATABASE_USER":     "master-username",
			"DATABASE_PASSWORD": "master-password",
		},
		SecretRefs: map[string]components.SecretRef{
			"master-username": components.RDSManagedKey(database.MasterSecretARN, "username"),
			"master-password": components.RDSManagedKey(database.MasterSecretARN, "password"),
		},
		LogGroup: logs.Migrate.Name,
	}, with(opts, pulumi.DependsOn(identities.MigrateDependencies))...)
	if err != nil {
		return nil, err
	}
	deployAccess.MigrateFamily = migrations.Family
	deployAccess.MigrateLogGroupARN = logs.Migrate.Arn
	// cd.yml registers a revision of the family, which passes its execution role again.
	deployAccess.PassRoleARNs = append(deployAccess.PassRoleARNs, identities.MigrateExecution.Arn)
{%- endif %}

	if _, err := components.NewDeployAccess(ctx, cfg, deployAccess, opts...); err != nil {
		return nil, err
	}

	// --- Outputs. These are the CD pipeline's configuration. ---------------------------------
	exports := Exports{
		"aws_account_id":   pulumi.String(cfg.AccountID),
		"aws_region":       pulumi.String(cfg.Region),
		"image_repo":       registry.URL,
		"deploy_role_arn":  identities.Deploy.Arn,
		"preview_role_arn": identities.Preview.Arn,
		// Read by no pipeline. Exported because it is the role an application grant goes on, and
		// the first thing anybody adding one asks for.
		"runtime_role_arn": identities.Runtime.Arn,
		// What the CI roles trust, so sync-github.sh can check it against the subject GitHub
		// actually issues for this repository before it un-gates the pipelines.
		"github_subject_prefix":   pulumi.String(cfg.SubjectPrefix()),
		"ecs_cluster":             cluster.Name,
		"ecs_service":             backend.Name,
		"ecs_service_arn":         backend.ARN,
		"service_url":             backend.URL,
		"ecs_task_subnets":        subnets.ToStringArrayOutput().ApplyT(joinComma).(pulumi.StringOutput),
		"ecs_task_security_group": network.AppSecurityGroup.ID().ToStringOutput(),
		// Empty unless the thing they name exists. sync-github.sh deletes the repository
		// variable for an empty output rather than setting it, and every workflow step that
		// uses one is gated on it — so turning a feature off un-configures its pipeline too,
		// instead of leaving it pointed at a resource that no longer exists.
		"ecs_migrate_task_family": pulumi.String(""),
		"rds_instance":            pulumi.String(""),
		// "true" once every secret the service cannot start without has a pinned version.
		// sync-github.sh refuses to un-gate CD until it is, because the image CD deploys would
		// refuse to start. `just infra-secrets` is what makes it true.
		"secrets_ready": pulumi.String(strconv.FormatBool(cfg.SecretsReady())),
	}
{%- if cookiecutter.use_postgres == "yes" %}
	exports["ecs_migrate_task_family"] = migrations.Family
	exports["rds_instance"] = database.Identifier
{%- endif %}

	return exports, nil
}

// joinComma is a subnet list as one repository variable: `aws ecs run-task` takes
// `subnets=[a,b]`, and a variable holds a string.
func joinComma(ids []string) string {
	return strings.Join(ids, ",")
}

// with returns opts plus more, without writing into opts' backing array — two slices built from
// one base must not share a tail.
func with(opts []pulumi.ResourceOption, more ...pulumi.ResourceOption) []pulumi.ResourceOption {
	return append(slices.Clone(opts), more...)
}
