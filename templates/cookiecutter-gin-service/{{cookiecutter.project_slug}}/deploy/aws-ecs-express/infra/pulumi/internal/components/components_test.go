// Component tests. No cloud credentials, no network, no state.
//
// What is worth testing here is not "does Pulumi work" but the handful of settings where a silent
// mistake is expensive: a CI role any repository can assume, a deploy role that can push anywhere,
// a secret read at whatever version is current, a database security group open to a CIDR, or a
// health check pointed at a path that is never going to answer.
package components_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"{{ cookiecutter.go_module }}/infra/pulumi/internal/components"
	"{{ cookiecutter.go_module }}/infra/pulumi/internal/infratest"
	"{{ cookiecutter.go_module }}/infra/pulumi/internal/stackconfig"
)

const (
	slug       = "{{ cookiecutter.project_slug }}"
	repository = "{{ cookiecutter.github_repository }}"

	serviceType = "aws:ecs/expressGatewayService:ExpressGatewayService"
	roleType    = "aws:iam/role:Role"
	policyType  = "aws:iam/rolePolicy:RolePolicy"
)

var cfg = stackconfig.Defaults(infratest.AccountID, infratest.Region, "dev", repository, slug)

func run(t *testing.T, body pulumi.RunFunc) *infratest.Recorder {
	t.Helper()
	return infratest.Run(t, slug, nil, body)
}

func newService(t *testing.T, args components.ServiceArgs) *infratest.Recorder {
	t.Helper()
	return run(t, func(ctx *pulumi.Context) error {
		_, err := components.NewService(ctx, cfg, args)
		return err
	})
}

func backendArgs() components.ServiceArgs {
	return components.ServiceArgs{
		App:                   slug,
		Cluster:               pulumi.String(slug + "-dev"),
		ExecutionRoleARN:      pulumi.String("arn:aws:iam::123456789012:role/exec"),
		InfrastructureRoleARN: pulumi.String("arn:aws:iam::123456789012:role/infra"),
		TaskRoleARN:           pulumi.String("arn:aws:iam::123456789012:role/runtime"),
		Port:                  8080,
		HealthPath:            "/health/live",
		Env:                   map[string]pulumi.StringInput{"ENVIRONMENT": pulumi.String("production")},
		LogGroup:              pulumi.String("/ecs/" + slug + "-dev"),
		Subnets:               pulumi.ToStringArray([]string{"subnet-a", "subnet-b"}),
		SecurityGroups:        pulumi.ToStringArray([]string{"sg-app"}),
	}
}

func container(t *testing.T, rec *infratest.Recorder, app string) any {
	t.Helper()
	c := infratest.Get(rec.Find(t, serviceType, app).Inputs, "primaryContainer")
	if c == nil {
		t.Fatalf("%s has no primary container", app)
	}
	return c
}

func identities(t *testing.T) *infratest.Recorder {
	t.Helper()
	return run(t, func(ctx *pulumi.Context) error {
		_, err := components.NewIdentities(ctx, cfg, components.IdentitiesArgs{
			SecretARNs:          []pulumi.StringOutput{pulumi.String("arn:secret:one").ToStringOutput()},
			GeneratedSecretARNs: []pulumi.StringOutput{pulumi.String("arn:secret:one").ToStringOutput()},
		})
		return err
	})
}

// statements decodes a policy document input into its statements.
func statements(t *testing.T, document any) []any {
	t.Helper()
	got, _ := infratest.Get(infratest.JSON(t, document), "Statement").([]any)
	if len(got) == 0 {
		t.Fatalf("no statements in %v", document)
	}
	return got
}

func strs(v any) []string {
	switch v := v.(type) {
	case string:
		return []string{v}
	case []any:
		out := make([]string, 0, len(v))
		for _, item := range v {
			s, _ := item.(string)
			out = append(out, s)
		}
		return out
	}
	return nil
}

// --- CI identities ----------------------------------------------------------------------------

func TestTheDeployRoleTrustsOnlyTheProductionEnvironmentOfOneRepository(t *testing.T) {
	// The subject is the one value that stops every other repository on GitHub — and every
	// workflow in this one that is not the deploy job — from assuming the deploy role.
	trust := statements(t, identities(t).Find(t, roleType, "deploy-role").Inputs["assumeRolePolicy"])
	if len(trust) != 1 {
		t.Fatalf("%d trust statements, want 1", len(trust))
	}
	if got := infratest.Get(trust[0], "Principal", "Federated"); got != infratest.OIDCProviderARN {
		t.Errorf("federated principal = %v, want the looked-up provider", got)
	}
	if got := infratest.Get(trust[0], "Action"); !slices.Equal(strs(got), []string{"sts:AssumeRoleWithWebIdentity"}) {
		t.Errorf("action = %v", got)
	}
	equals := infratest.Get(trust[0], "Condition", "StringEquals")
	if got := infratest.Get(equals, "token.actions.githubusercontent.com:sub"); got != "repo:"+repository+":environment:production" {
		t.Errorf("sub = %v", got)
	}
	if got := infratest.Get(equals, "token.actions.githubusercontent.com:aud"); got != "sts.amazonaws.com" {
		t.Errorf("aud = %v", got)
	}
	if infratest.Get(trust[0], "Condition", "StringLike") != nil {
		t.Error("a StringLike subject is a wildcard waiting to admit the whole organisation")
	}
}

func TestThePreviewRoleTrustsOnlyPullRequestsOfOneRepository(t *testing.T) {
	trust := statements(t, identities(t).Find(t, roleType, "preview-role").Inputs["assumeRolePolicy"])
	equals := infratest.Get(trust[0], "Condition", "StringEquals")
	if got := infratest.Get(equals, "token.actions.githubusercontent.com:sub"); got != "repo:"+repository+":pull_request" {
		t.Errorf("sub = %v", got)
	}
	if got := infratest.Get(equals, "token.actions.githubusercontent.com:aud"); got != "sts.amazonaws.com" {
		t.Errorf("aud = %v", got)
	}
}

func TestBothRolesTrustTheImmutableSubjectWhenItIsConfigured(t *testing.T) {
	// GitHub issues `repo:<owner>@<id>/<repo>@<id>` for many repositories; a role that trusts the
	// name-only form then admits no token at all.
	immutable := cfg
	immutable.GitHubSubjectPrefix = "repo:acme@123/billing-api@456"
	rec := run(t, func(ctx *pulumi.Context) error {
		_, err := components.NewIdentities(ctx, immutable, components.IdentitiesArgs{
			SecretARNs:          []pulumi.StringOutput{pulumi.String("arn:secret:one").ToStringOutput()},
			GeneratedSecretARNs: []pulumi.StringOutput{pulumi.String("arn:secret:one").ToStringOutput()},
		})
		return err
	})
	for role, want := range map[string]string{
		"deploy-role":  "repo:acme@123/billing-api@456:environment:production",
		"preview-role": "repo:acme@123/billing-api@456:pull_request",
	} {
		trust := statements(t, rec.Find(t, roleType, role).Inputs["assumeRolePolicy"])
		if got := infratest.Get(trust[0], "Condition", "StringEquals", "token.actions.githubusercontent.com:sub"); got != want {
			t.Errorf("%s sub = %v, want %s", role, got, want)
		}
	}
}

func TestTheOIDCProviderIsLookedUpNotCreated(t *testing.T) {
	// One per account, shared by every service in it. A stack that created it would delete it
	// from under its neighbours on destroy.
	rec := identities(t)
	if got := rec.OfType("aws:iam/openIdConnectProvider:OpenIdConnectProvider"); len(got) != 0 {
		t.Error("the stack creates the account's OIDC provider; the bootstrap script owns it")
	}
	if !slices.ContainsFunc(rec.Invokes(), func(i infratest.Invoke) bool {
		return i.Token == "aws:iam/getOpenIdConnectProvider:getOpenIdConnectProvider" &&
			i.Args["url"] == components.GitHubOIDCURL
	}) {
		t.Error("the provider is not looked up by GitHub's issuer URL")
	}
}

func TestThePreviewRoleWritesNothingButItsOwnLocks(t *testing.T) {
	// A pull request's preview reads the account and this project's state. It must not be able to
	// change either — the one write is the backend's lock object, under this project's prefix.
	rec := identities(t)
	rec.Find(t, "aws:iam/rolePolicyAttachment:RolePolicyAttachment", "preview-read-only")
	for _, statement := range statements(t, rec.Find(t, policyType, "preview-reads-state").Inputs["policy"]) {
		for _, action := range strs(infratest.Get(statement, "Action")) {
			resources := strs(infratest.Get(statement, "Resource"))
			switch action {
			case "s3:PutObject", "s3:DeleteObject":
				for _, resource := range resources {
					if !strings.HasSuffix(resource, "/.pulumi/locks/organization/"+slug+"/*") {
						t.Errorf("%s on %s, outside this project's locks", action, resource)
					}
				}
			case "s3:GetObject", "s3:ListBucket", "kms:Decrypt", "kms:GenerateDataKey", "secretsmanager:GetSecretValue":
			default:
				t.Errorf("the preview role may %s", action)
			}
			if action == "s3:GetObject" {
				for _, resource := range resources {
					if strings.Contains(resource, "/.pulumi/stacks/") && !strings.Contains(resource, "/.pulumi/stacks/"+slug+"/") {
						t.Errorf("the preview reads another project's state: %s", resource)
					}
				}
			}
			if strings.HasPrefix(action, "kms:") && !slices.Equal(resources, []string{infratest.StateKeyARN}) {
				t.Errorf("%s on %v, not the state key", action, resources)
			}
		}
	}
}

func TestThePreviewReadsOnlyTheSecretsTheStackGenerated(t *testing.T) {
	// Refresh reads a SecretVersion's value; those values are already in state. A manual secret's
	// value is not, and must stay out of the preview's reach.
	rec := run(t, func(ctx *pulumi.Context) error {
		_, err := components.NewIdentities(ctx, cfg, components.IdentitiesArgs{
			SecretARNs: []pulumi.StringOutput{
				pulumi.String("arn:generated").ToStringOutput(), pulumi.String("arn:manual").ToStringOutput(),
			},
			GeneratedSecretARNs: []pulumi.StringOutput{pulumi.String("arn:generated").ToStringOutput()},
		})
		return err
	})
	for _, statement := range statements(t, rec.Find(t, policyType, "preview-reads-state").Inputs["policy"]) {
		if slices.Contains(strs(infratest.Get(statement, "Action")), "secretsmanager:GetSecretValue") {
			if got := strs(infratest.Get(statement, "Resource")); !slices.Equal(got, []string{"arn:generated"}) {
				t.Errorf("the preview may read %v", got)
			}
		}
	}
}

func TestTheDeployRoleCanReachOnlyThisStacksRepositoriesAndServices(t *testing.T) {
	// ecr:GetAuthorizationToken is a login with no resource-level permission; it is the one ECR
	// action allowed on `*`. Everything else names a repository, a service or a role of this stack.
	got := components.DeployStatements(cfg, components.DeployTargets{
		RepositoryARNs:     []string{"arn:aws:ecr:eu-west-1:123456789012:repository/" + slug + "-dev"},
		ServiceARNs:        []string{"arn:aws:ecs:eu-west-1:123456789012:service/" + slug + "-dev/" + slug + "-dev"},
		Cluster:            slug + "-dev",
		MigrateFamily:      slug + "-migrate-dev",
		MigrateLogGroupARN: "arn:aws:logs:eu-west-1:123456789012:log-group:/ecs/" + slug + "-migrate-dev",
		PassRoleARNs:       []string{"arn:aws:iam::123456789012:role/exec"},
	})
	unscoped := map[string]bool{
		"ecr:GetAuthorizationToken":  true,
		"ecs:DescribeTaskDefinition": true,
		"ecs:RegisterTaskDefinition": true,
	}
	for _, statement := range got {
		for _, action := range statement.Action {
			if strings.Contains(action, "*") {
				t.Errorf("wildcard action %s", action)
			}
			if slices.Contains(statement.Resource, "*") && !unscoped[action] {
				t.Errorf("%s is granted on *", action)
			}
		}
		if slices.Contains(statement.Action, "iam:PassRole") {
			passed := statement.Condition["StringEquals"]["iam:PassedToService"]
			if !slices.Equal(passed.([]string), components.PassedToServices) {
				t.Errorf("PassRole is not limited to ECS: %v", statement.Condition)
			}
		}
		if slices.Contains(statement.Action, "ecs:RunTask") &&
			!slices.Equal(statement.Resource, []string{"arn:aws:ecs:eu-west-1:123456789012:task-definition/" + slug + "-migrate-dev:*"}) {
			t.Errorf("RunTask on %v, not the migration family", statement.Resource)
		}
	}
}

func TestEveryRoleNameFitsIAMsLimit(t *testing.T) {
	long := cfg
	long.Slug = "a-very-long-service-name-that-goes-on-and-on-and-on-for-ever"
	for _, role := range []string{"ecs-infra", "exec", "runtime", "deploy", "preview"} {
		name, err := long.RoleName(role)
		if err != nil {
			t.Fatal(err)
		}
		if len(name) > stackconfig.MaxRoleName || !strings.HasSuffix(name, "-"+role+"-dev") {
			t.Errorf("role name %q", name)
		}
	}
}

// --- ECS identities ---------------------------------------------------------------------------

func TestTheTaskRolesCanOnlyBeAssumedByTasksInThisAccount(t *testing.T) {
	rec := identities(t)
	for _, name := range []string{"execution-role", "runtime-role"} {
		trust := statements(t, rec.Find(t, roleType, name).Inputs["assumeRolePolicy"])
		if got := infratest.Get(trust[0], "Principal", "Service"); got != "ecs-tasks.amazonaws.com" {
			t.Errorf("%s trusts %v", name, got)
		}
		if got := infratest.Get(trust[0], "Condition", "StringEquals", "aws:SourceAccount"); got != infratest.AccountID {
			t.Errorf("%s has no aws:SourceAccount guard: %v", name, got)
		}
	}
	infra := statements(t, rec.Find(t, roleType, "infrastructure-role").Inputs["assumeRolePolicy"])
	if got := infratest.Get(infra[0], "Principal", "Service"); got != "ecs.amazonaws.com" {
		t.Errorf("infrastructure role trusts %v", got)
	}
}

func TestTheRuntimeRoleHoldsNothing(t *testing.T) {
	// The application's own identity starts empty; a grant is added when the application needs
	// one, on purpose, in review.
	rec := identities(t)
	runtime := rec.Find(t, roleType, "runtime-role")
	for _, attachment := range rec.OfType("aws:iam/rolePolicyAttachment:RolePolicyAttachment") {
		if attachment.Inputs["role"] == runtime.Inputs["name"] {
			t.Errorf("the runtime role has %v attached", attachment.Inputs["policyArn"])
		}
	}
	for _, policy := range rec.OfType(policyType) {
		if policy.Inputs["role"] == runtime.Inputs["name"] {
			t.Errorf("the runtime role has an inline policy %s", policy.Name)
		}
	}
}

func TestTheExecutionRoleReadsExactlyTheStacksSecrets(t *testing.T) {
	rec := identities(t)
	got := statements(t, rec.Find(t, policyType, "execution-reads-secrets").Inputs["policy"])
	if len(got) != 1 || !slices.Equal(strs(infratest.Get(got[0], "Resource")), []string{"arn:secret:one"}) {
		t.Errorf("execution secrets policy = %v", got)
	}
}

// --- Express service shape --------------------------------------------------------------------

func TestTheHealthCheckReadsLivenessAndTheContainerPort(t *testing.T) {
	rec := newService(t, backendArgs())
	svc := rec.Find(t, serviceType, slug)
	if got := svc.Inputs["healthCheckPath"]; got != "/health/live" {
		t.Errorf("healthCheckPath = %v", got)
	}
	if got := infratest.Get(container(t, rec, slug), "containerPort"); got != 8080.0 {
		t.Errorf("containerPort = %v", got)
	}
}

func TestThePipelineOwnsTheImageAndNothingElseDoes(t *testing.T) {
	// Two tools writing one field is how an infrastructure repository stops being trusted. The
	// path is what the engine receives, read off the registration rather than off a constant.
	rec := newService(t, backendArgs())
	svc := rec.Find(t, serviceType, slug)
	if !slices.Equal(svc.IgnoreChanges, []string{"primaryContainer.image"}) {
		t.Errorf("ignoreChanges = %v", svc.IgnoreChanges)
	}
	if got := infratest.Get(container(t, rec, slug), "image"); got != components.BootstrapImage {
		t.Errorf("image = %v; the stack declares the placeholder and CD replaces it", got)
	}
}

func TestTheServiceRunsInTheGivenSubnetsAsTheAppGroup(t *testing.T) {
	svc := newService(t, backendArgs()).Find(t, serviceType, slug)
	network := infratest.Get(svc.Inputs, "networkConfigurations", 0)
	if got := strs(infratest.Get(network, "securityGroups")); !slices.Equal(got, []string{"sg-app"}) {
		t.Errorf("securityGroups = %v", got)
	}
	if got := strs(infratest.Get(network, "subnets")); len(got) < 2 {
		t.Errorf("subnets = %v; Express Mode needs at least two", got)
	}
}

func TestTheServiceScalesOnCPUWithinTheConfiguredRange(t *testing.T) {
	scaling := infratest.Get(newService(t, backendArgs()).Find(t, serviceType, slug).Inputs, "scalingTargets", 0)
	want := map[string]any{
		"minTaskCount":           1.0,
		"maxTaskCount":           4.0,
		"autoScalingMetric":      "AVERAGE_CPU",
		"autoScalingTargetValue": 60.0,
	}
	for key, value := range want {
		if got := infratest.Get(scaling, key); got != value {
			t.Errorf("%s = %v, want %v", key, got, value)
		}
	}
}

func TestTheServiceLogsToTheStacksOwnGroup(t *testing.T) {
	// Express Mode's default group never expires and outlives the service.
	logs := infratest.Get(container(t, newService(t, backendArgs()), slug), "awsLogsConfigurations", 0)
	if got := infratest.Get(logs, "logGroup"); got != "/ecs/"+slug+"-dev" {
		t.Errorf("logGroup = %v", got)
	}
}

func TestAServiceRefusesASecretItWasNeverGiven(t *testing.T) {
	args := backendArgs()
	args.SecretEnv = map[string]string{"DATABASE_URL_OVERRIDE": "typo"}
	args.SecretRefs = map[string]components.SecretRef{
		"database-url": {ARN: pulumi.String("x"), VersionID: pulumi.String("1")},
	}
	_, err := infratest.TryRun(slug, nil, func(ctx *pulumi.Context) error {
		_, err := components.NewService(ctx, cfg, args)
		return err
	})
	if err == nil || !strings.Contains(err.Error(), `"typo"`) {
		t.Fatalf("err = %v, want one naming the missing secret", err)
	}
}

func TestAMountedSecretIsPinnedToAVersionID(t *testing.T) {
	// `<arn>:::<version-id>` — the JSON key and the stage left empty, the version named. A task
	// reads its secrets when it starts, so a stage-relative reference lets two tasks of one
	// deployment hold different values after a rotation.
	args := backendArgs()
	args.SecretEnv = map[string]string{"JWT_SECRET": "jwt-secret"}
	args.SecretRefs = map[string]components.SecretRef{
		"jwt-secret": {ARN: pulumi.String("arn:aws:secretsmanager:eu-west-1:123456789012:secret:jwt"), VersionID: pulumi.String("v7")},
	}
	env := infratest.EnvOf(container(t, newService(t, args), slug))
	if got := env["JWT_SECRET"]; got != "secret:arn:aws:secretsmanager:eu-west-1:123456789012:secret:jwt:::v7" {
		t.Errorf("JWT_SECRET = %q", got)
	}
}

func TestTheEnvironmentIsInTheSameOrderEveryRun(t *testing.T) {
	args := backendArgs()
	args.Env = map[string]pulumi.StringInput{}
	for _, name := range []string{"ZULU", "ALPHA", "MIKE", "BRAVO", "YANKEE"} {
		args.Env[name] = pulumi.String(name)
	}
	envs, _ := infratest.Get(container(t, newService(t, args), slug), "environments").([]any)
	var names []string
	for _, env := range envs {
		names = append(names, infratest.Get(env, "name").(string))
	}
	if !slices.IsSorted(names) {
		t.Errorf("env order = %v", names)
	}
}

func TestTheEndpointBecomesAnHTTPSURLOnce(t *testing.T) {
	for endpoint, want := range map[string]string{
		"api.ecs.eu-west-1.on.aws":         "https://api.ecs.eu-west-1.on.aws",
		"https://api.ecs.eu-west-1.on.aws": "https://api.ecs.eu-west-1.on.aws",
	} {
		if got := components.EndpointURL(endpoint); got != want {
			t.Errorf("EndpointURL(%q) = %q", endpoint, got)
		}
	}
}

// --- Secrets ----------------------------------------------------------------------------------

func TestEverySecretWithoutAVersionIsDeclaredManual(t *testing.T) {
	var secrets *components.Secrets
	run(t, func(ctx *pulumi.Context) error {
		var err error
		secrets, err = components.NewSecrets(ctx, cfg, components.SecretsArgs{
			Generated: map[string]pulumi.StringInput{"generated-one": pulumi.String("minted-by-the-stack")},
		})
		return err
	})
	if len(secrets.Versions) != len(secrets.ARNs)-len(components.ManualSecrets) {
		t.Errorf("%d versions for %d secrets and %d manual secrets",
			len(secrets.Versions), len(secrets.ARNs), len(components.ManualSecrets))
	}
	for _, name := range components.ManualSecrets {
		if _, ok := secrets.Refs[name]; ok {
			t.Errorf("manual secret %s has a reference before anybody populated it", name)
		}
	}
}

func TestAGeneratedSecretValueIsMarkedSecret(t *testing.T) {
	rec := run(t, func(ctx *pulumi.Context) error {
		_, err := components.NewSecrets(ctx, cfg, components.SecretsArgs{
			Generated: map[string]pulumi.StringInput{"token": pulumi.String("value")},
		})
		return err
	})
	version := rec.Find(t, "aws:secretsmanager/secretVersion:SecretVersion", "token-version")
	if version.Inputs["secretString"] != "value" {
		t.Fatalf("secretString = %v", version.Inputs["secretString"])
	}
	if !slices.Contains(version.Secret, "secretString") {
		t.Error("secretString reaches the provider as plaintext rather than as a secret")
	}
}

func TestASecretIsDeletedAtOnceOutsideProductionAndRecoverableInIt(t *testing.T) {
	// A non-zero window outside production reserves the name for a month, and the next `up`
	// after a destroy fails on it.
	for env, want := range map[string]float64{"dev": 0, "prod": components.RecoveryWindowDays} {
		c := cfg
		c.Environment = env
		rec := run(t, func(ctx *pulumi.Context) error {
			_, err := components.NewSecrets(ctx, c, components.SecretsArgs{
				Generated: map[string]pulumi.StringInput{"token": pulumi.String("value")},
			})
			return err
		})
		secret := rec.Find(t, "aws:secretsmanager/secret:Secret", "token")
		if got := secret.Inputs["recoveryWindowInDays"]; got != want {
			t.Errorf("%s: recoveryWindowInDays = %v, want %v", env, got, want)
		}
		if got := secret.Inputs["name"]; got != slug+"-token-"+env {
			t.Errorf("%s: name = %v", env, got)
		}
	}
}

// --- Registry, logs, cluster ------------------------------------------------------------------

func TestEveryRepositoryIsImmutableAndScanned(t *testing.T) {
	rec := run(t, func(ctx *pulumi.Context) error {
		_, err := components.NewRegistry(ctx, cfg)
		return err
	})
	repositories := rec.OfType("aws:ecr/repository:Repository")
	if len(repositories) != 1 {
		t.Fatalf("%d repositories, want 1", len(repositories))
	}
	for _, repository := range repositories {
		if repository.Inputs["imageTagMutability"] != "IMMUTABLE" {
			t.Errorf("%s: tags are mutable", repository.Name)
		}
		if infratest.Get(repository.Inputs, "imageScanningConfiguration", "scanOnPush") != true {
			t.Errorf("%s: no scan on push", repository.Name)
		}
		if repository.Inputs["forceDelete"] != true {
			t.Errorf("%s: a dev destroy stops on a repository with images in it", repository.Name)
		}
	}
	for _, policy := range rec.OfType("aws:ecr/lifecyclePolicy:LifecyclePolicy") {
		rule := infratest.Get(infratest.JSON(t, policy.Inputs["policy"]), "rules", 0, "selection")
		if infratest.Get(rule, "countNumber") != float64(components.KeepImages) {
			t.Errorf("%s keeps %v images", policy.Name, infratest.Get(rule, "countNumber"))
		}
	}
}

func TestProductionImagesSurviveADestroy(t *testing.T) {
	prod := cfg
	prod.Environment = "prod"
	rec := run(t, func(ctx *pulumi.Context) error {
		_, err := components.NewRegistry(ctx, prod)
		return err
	})
	if rec.Find(t, "aws:ecr/repository:Repository", "images").Inputs["forceDelete"] != false {
		t.Error("a prod destroy deletes the images a rollback would run")
	}
}

func TestEveryLogGroupExpires(t *testing.T) {
	for env, want := range map[string]float64{"dev": components.LogRetentionDays, "prod": components.ProductionLogRetentionDays} {
		c := cfg
		c.Environment = env
		rec := run(t, func(ctx *pulumi.Context) error {
			_, err := components.NewLogs(ctx, c, components.LogsArgs{Migrate: true})
			return err
		})
		groups := rec.OfType("aws:cloudwatch/logGroup:LogGroup")
		if len(groups) != 2 {
			t.Errorf("%s: %d log groups, want 2", env, len(groups))
		}
		for _, group := range groups {
			if group.Inputs["retentionInDays"] != want {
				t.Errorf("%s: %v retains for %v days", env, group.Inputs["name"], group.Inputs["retentionInDays"])
			}
		}
	}
}

func TestContainerInsightsIsAProductionCost(t *testing.T) {
	for env, want := range map[string]string{"dev": "disabled", "prod": "enhanced"} {
		c := cfg
		c.Environment = env
		rec := run(t, func(ctx *pulumi.Context) error {
			_, err := components.NewCluster(ctx, c)
			return err
		})
		setting := infratest.Get(rec.Find(t, "aws:ecs/cluster:Cluster", "cluster").Inputs, "settings", 0)
		if infratest.Get(setting, "name") != "containerInsights" || infratest.Get(setting, "value") != want {
			t.Errorf("%s: settings = %v", env, setting)
		}
	}
}

// --- Network ----------------------------------------------------------------------------------

func network(t *testing.T, c stackconfig.StackConfig) *infratest.Recorder {
	t.Helper()
	return run(t, func(ctx *pulumi.Context) error {
		_, err := components.NewNetwork(ctx, c)
		return err
	})
}

func TestTheNetworkHasPublicSubnetsAndIsolatedOnesInTheFirstZones(t *testing.T) {
	rec := network(t, cfg)
	subnets := rec.OfType("aws:ec2/subnet:Subnet")
	if len(subnets) != 2*cfg.AvailabilityZoneCount {
		t.Fatalf("%d subnets, want %d", len(subnets), 2*cfg.AvailabilityZoneCount)
	}
	// The mock returns zones out of order; the stack sorts them, so subnet 0 is in zone a.
	for _, name := range []string{"public-0", "isolated-0"} {
		if got := rec.Find(t, "aws:ec2/subnet:Subnet", name).Inputs["availabilityZone"]; got != infratest.Region+"a" {
			t.Errorf("%s is in %v, want the first zone by name", name, got)
		}
	}
	if rec.Find(t, "aws:ec2/subnet:Subnet", "public-0").Inputs["mapPublicIpOnLaunch"] != true {
		t.Error("tasks in a public subnet with no NAT need a public IP to pull their image")
	}
	if rec.Find(t, "aws:ec2/subnet:Subnet", "isolated-0").Inputs["mapPublicIpOnLaunch"] != false {
		t.Error("an isolated subnet hands out public IPs")
	}
}

func TestThereIsNoNATAndTheIsolatedSubnetsHaveNoRouteOut(t *testing.T) {
	rec := network(t, cfg)
	if got := rec.OfType("aws:ec2/natGateway:NatGateway"); len(got) != 0 {
		t.Error("a NAT gateway is the largest line on a small service's bill")
	}
	routes := rec.OfType("aws:ec2/route:Route")
	if len(routes) != 1 || routes[0].Name != "public-internet" {
		t.Errorf("routes = %v; only the public table routes anywhere", routes)
	}
	if routes := infratest.Get(rec.Find(t, "aws:ec2/routeTable:RouteTable", "isolated").Inputs, "routes"); routes != nil {
		if items, _ := routes.([]any); len(items) != 0 {
			t.Errorf("the isolated table has routes: %v", routes)
		}
	}
}

func TestTheDatabaseGroupAdmitsPostgresFromTheAppGroupOnly(t *testing.T) {
	rec := network(t, cfg)
	ingress := rec.OfType("aws:vpc/securityGroupIngressRule:SecurityGroupIngressRule")
	if len(ingress) != 1 {
		t.Fatalf("%d ingress rules, want exactly the one into the database", len(ingress))
	}
	rule := ingress[0].Inputs
	if rule["referencedSecurityGroupId"] != "app-id" || rule["securityGroupId"] != "db-id" {
		t.Errorf("ingress rule = %v", rule)
	}
	if rule["fromPort"] != 5432.0 || rule["toPort"] != 5432.0 || rule["ipProtocol"] != "tcp" {
		t.Errorf("ingress rule = %v", rule)
	}
	if rule["cidrIpv4"] != nil {
		t.Error("a CIDR rule admits whatever lands in those subnets next")
	}
	for _, egress := range rec.OfType("aws:vpc/securityGroupEgressRule:SecurityGroupEgressRule") {
		if egress.Inputs["securityGroupId"] == "db-id" {
			t.Error("the database group has an egress rule")
		}
	}
	for _, group := range rec.OfType("aws:ec2/securityGroup:SecurityGroup") {
		if group.Inputs["ingress"] != nil || group.Inputs["egress"] != nil {
			t.Errorf("%s has inline rules, which fight the standalone ones", group.Name)
		}
	}
}

func TestAThirdZoneIsAThirdPairOfSubnets(t *testing.T) {
	three := cfg
	three.AvailabilityZoneCount = 3
	rec := network(t, three)
	if got := rec.Find(t, "aws:ec2/subnet:Subnet", "isolated-2").Inputs["cidrBlock"]; got != "10.0.102.0/24" {
		t.Errorf("isolated-2 = %v", got)
	}
	if got := rec.Find(t, "aws:ec2/subnet:Subnet", "public-2").Inputs["availabilityZone"]; got != infratest.Region+"c" {
		t.Errorf("public-2 is in %v", got)
	}
}
