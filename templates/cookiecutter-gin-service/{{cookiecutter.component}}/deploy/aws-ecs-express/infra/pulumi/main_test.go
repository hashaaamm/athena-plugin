// Composition-root tests: the whole stack, built against mocks, in each configuration it ships
// with. No cloud credentials, no network, no state.
package main

import (
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"{{ cookiecutter.go_module }}/infra/pulumi/internal/infratest"
	"{{ cookiecutter.go_module }}/infra/pulumi/internal/stackconfig"
)

const (
	slug       = "{{ cookiecutter.__name }}"
	repository = "{{ cookiecutter.github_repository }}"

	serviceType = "aws:ecs/expressGatewayService:ExpressGatewayService"
	roleType    = "aws:iam/role:Role"
)

// allExports is every name the stack exports, in every configuration.
var allExports = []string{
	"apply_role_arn",
	"aws_account_id",
	"aws_region",
	"deploy_role_arn",
	"ecs_cluster",
	"ecs_migrate_task_family",
	"ecs_service",
	"ecs_service_arn",
	"ecs_task_security_group",
	"ecs_task_subnets",
	"github_subject_prefix",
	"image_repo",
	"preview_role_arn",
	"rds_instance",
	"runtime_role_arn",
	"secrets_ready",
	"service_url",
}

// pipelineExports are the ones cd.yml and infra.yml cannot run without.
var pipelineExports = []string{
	"aws_account_id", "aws_region", "image_repo", "deploy_role_arn", "preview_role_arn", "apply_role_arn",
	"ecs_cluster", "ecs_service", "ecs_service_arn", "service_url", "ecs_task_subnets",
	"ecs_task_security_group",
}

// firstImage is what `just infra-first-deploy` records as serviceImage: the registry's own path
// and the commit it was built from.
const firstImage = infratest.AccountID + ".dkr.ecr." + infratest.Region + ".amazonaws.com/" + slug +
	"-dev:0123456789abcdef0123456789abcdef01234567"

// baseConfig is the stack after its first deploy, which is the state it spends its life in.
func baseConfig() stackconfig.StackConfig {
	c := stackconfig.Defaults(infratest.AccountID, infratest.Region, "dev", repository, slug)
	c.ServiceImage = firstImage
	return c
}

// beforeFirstDeploy is the stack `just infra-up` applies first: no serviceImage yet.
func beforeFirstDeploy(c *stackconfig.StackConfig) {
	c.ServiceImage = ""
}

// stack is one mocked run of deploy: what it registered and what it exported.
type stack struct {
	*infratest.Recorder
	exports map[string]string
}

func deployed(t *testing.T, mutate func(*stackconfig.StackConfig)) stack {
	t.Helper()
	cfg := baseConfig()
	if mutate != nil {
		mutate(&cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	exports := map[string]string{}
	rec := infratest.Run(t, slug, nil, func(ctx *pulumi.Context) error {
		out, err := deploy(ctx, cfg)
		if err != nil {
			return err
		}
		// Exported, as main does, and not merely applied: an export is awaited before the program
		// returns, but an apply on a plain value such as pulumi.String(cfg.Region) belongs to no
		// resource and may still be pending when it does.
		for name, value := range out {
			ctx.Export(name, value.ToStringOutput().ApplyT(func(v string) string {
				mu.Lock()
				defer mu.Unlock()
				exports[name] = v
				return v
			}))
		}
		return nil
	})
	return stack{Recorder: rec, exports: exports}
}

// browserOrigin is where a browser client generated from cookiecutter-react-app is served.
const browserOrigin = "https://d1234abcd.cloudfront.net"

// withBrowserClient is the configuration infra/README.md gives for pointing a browser client at
// this API.
func withBrowserClient(c *stackconfig.StackConfig) {
	c.CORSOrigins = []string{browserOrigin, "http://localhost:3000"}
{%- if cookiecutter.use_postgres == "yes" %}
	c.RefreshTransport = "cookie"
{%- endif %}
}

func (s stack) container(t *testing.T, app string) any {
	t.Helper()
	return infratest.Get(s.Find(t, serviceType, app).Inputs, "primaryContainer")
}

func (s stack) backendEnv(t *testing.T) map[string]string {
	t.Helper()
	return infratest.EnvOf(s.container(t, slug))
}

// --- Exports ----------------------------------------------------------------------------------

func TestEveryExportExistsInEveryConfiguration(t *testing.T) {
	// A name that is exported in one configuration and absent in another is indistinguishable,
	// to sync-github.sh, from a stack that was never applied.
	for _, mutate := range []func(*stackconfig.StackConfig){nil, withBrowserClient, beforeFirstDeploy} {
		got := slices.Sorted(maps.Keys(deployed(t, mutate).exports))
		if !slices.Equal(got, allExports) {
			t.Errorf("exports = %v\nwant      %v", got, allExports)
		}
	}
}

func TestThePipelineExportsAreNeverEmpty(t *testing.T) {
	s := deployed(t, nil)
	for _, name := range pipelineExports {
		if s.exports[name] == "" {
			t.Errorf("%s is empty", name)
		}
	}
	if want := infratest.AccountID + ".dkr.ecr." + infratest.Region + ".amazonaws.com/" + slug + "-dev"; s.exports["image_repo"] != want {
		t.Errorf("image_repo = %q, want %q", s.exports["image_repo"], want)
	}
	if !strings.HasPrefix(s.exports["service_url"], "https://") {
		t.Errorf("service_url = %q is not an https URL", s.exports["service_url"])
	}
	if got := strings.Split(s.exports["ecs_task_subnets"], ","); len(got) != 2 {
		t.Errorf("ecs_task_subnets = %q, want the two public subnets", s.exports["ecs_task_subnets"])
	}
	if s.exports["ecs_task_security_group"] != "app-id" {
		t.Errorf("ecs_task_security_group = %q, want the app group", s.exports["ecs_task_security_group"])
	}
}

// --- The first deploy -------------------------------------------------------------------------

func TestBeforeTheFirstDeployThereIsNoServiceAndNoMigrationTask(t *testing.T) {
	// There is no placeholder image. `just infra-up` creates everything the first deploy needs —
	// the registry it pushes to, the roles, the database — and nothing that would have to run
	// somebody else's container.
	s := deployed(t, beforeFirstDeploy)
	if services := s.OfType(serviceType); len(services) != 0 {
		t.Errorf("%d Express services before the first deploy, want none", len(services))
	}
	if tasks := s.OfType("aws:ecs/taskDefinition:TaskDefinition"); len(tasks) != 0 {
		t.Errorf("%d task definitions before the first deploy, want none", len(tasks))
	}
	// Empty, so sync-github.sh refuses to un-gate a CD that could only update a service that
	// does not exist.
	for _, name := range []string{"ecs_service", "ecs_service_arn", "service_url", "ecs_migrate_task_family"} {
		if got := s.exports[name]; got != "" {
			t.Errorf("%s = %q before the first deploy, want empty", name, got)
		}
	}
	for _, name := range []string{"image_repo", "deploy_role_arn", "ecs_cluster", "ecs_task_subnets", "secrets_ready"} {
		if s.exports[name] == "" {
			t.Errorf("%s is empty; the first deploy needs it", name)
		}
	}
	// IAM refuses a statement with no resource, so the deploy role grants nothing on services
	// until there is one.
	policy, _ := s.Find(t, "aws:iam/rolePolicy:RolePolicy", "deploy-permissions").Inputs["policy"].(string)
	if strings.Contains(policy, "RollOwnServices") || strings.Contains(policy, `"Resource":null`) {
		t.Errorf("the deploy policy names services before there are any: %s", policy)
	}
}

func TestTheServiceIsCreatedOnTheFirstImage(t *testing.T) {
	s := deployed(t, nil)
	if got := infratest.Get(s.container(t, slug), "image"); got != firstImage {
		t.Errorf("image = %v, want serviceImage %q", got, firstImage)
	}
}

// --- Secrets ----------------------------------------------------------------------------------

func TestTheStackWritesNoSecretValue(t *testing.T) {
	// Pulumi standards MUST-3: no secret value in the program, its configuration or its state. A
	// generated value is in state like any other, so the stack generates none and writes no
	// version; `just infra-secrets` does both, out of band.
	for _, mutate := range []func(*stackconfig.StackConfig){nil, withBrowserClient} {
		for _, res := range deployed(t, mutate).Resources() {
			if strings.HasPrefix(res.Type, "random:") || res.Type == "aws:secretsmanager/secretVersion:SecretVersion" {
				t.Errorf("%s %q puts a secret value in state", res.Type, res.Name)
			}
			if len(res.Secret) != 0 {
				t.Errorf("%s %q has secret inputs %v", res.Type, res.Name, res.Secret)
			}
		}
	}
}

// --- Browser clients --------------------------------------------------------------------------

func TestTheStackServesOneServiceFromOneRepository(t *testing.T) {
	// Backend only: a browser client is its own repository with its own stack (ADR-0022), so it
	// adds nothing here whatever this stack is configured with.
	for _, mutate := range []func(*stackconfig.StackConfig){nil, withBrowserClient} {
		s := deployed(t, mutate)
		if services := s.OfType(serviceType); len(services) != 1 {
			t.Errorf("%d Express services, want 1", len(services))
		}
		if repositories := s.OfType("aws:ecr/repository:Repository"); len(repositories) != 1 {
			t.Errorf("%d ECR repositories, want 1", len(repositories))
		}
	}
}

func TestByDefaultNoBrowserMayCallTheAPI(t *testing.T) {
	// CORS_ORIGINS unset is the application's own "no cross-origin access". An empty value would
	// mean the same today and is one parsing change away from meaning something else.
	if got, ok := deployed(t, nil).backendEnv(t)["CORS_ORIGINS"]; ok {
		t.Errorf("CORS_ORIGINS = %q on a stack with no corsOrigins", got)
	}
}

func TestConfiguredOriginsReachTheBackend(t *testing.T) {
	// Otherwise the SPA loads, makes exactly one request, and the browser refuses the answer —
	// which reads as an API outage rather than as a missing allowlist entry.
	want := browserOrigin + ",http://localhost:3000"
	if got := deployed(t, withBrowserClient).backendEnv(t)["CORS_ORIGINS"]; got != want {
		t.Errorf("CORS_ORIGINS = %q, want %q", got, want)
	}
}

// --- The two environments ---------------------------------------------------------------------

func TestTheApplicationEnvironmentIsConfiguredRatherThanHardCoded(t *testing.T) {
	if got := deployed(t, nil).backendEnv(t)["ENVIRONMENT"]; got != "staging" {
		t.Errorf("ENVIRONMENT = %q, want the staging default", got)
	}
	prod := deployed(t, func(c *stackconfig.StackConfig) { c.AppEnvironment = "production" })
	if got := prod.backendEnv(t)["ENVIRONMENT"]; got != "production" {
		t.Errorf("ENVIRONMENT = %q, want production", got)
	}
}

func TestTheBackendRunsWithStructuredLogs(t *testing.T) {
	env := deployed(t, nil).backendEnv(t)
	if env["JSON_LOGS"] != "true" || env["LOG_LEVEL"] != "INFO" {
		t.Errorf("env = %v", env)
	}
	if _, ok := env["PORT"]; ok {
		t.Error("PORT is set; the image sets 8080 and the container port says so")
	}
}

// --- Wiring -----------------------------------------------------------------------------------

func TestEveryAWSResourceAndInvokeUsesTheExplicitProvider(t *testing.T) {
	// Pulumi.yaml disables the default AWS provider. A resource or a lookup that did not inherit
	// the explicit one would fail at apply time with "default provider disabled" — here, it fails
	// the build.
	for _, mutate := range []func(*stackconfig.StackConfig){nil, withBrowserClient} {
		s := deployed(t, mutate)
		for _, res := range s.Resources() {
			if res.Custom && strings.HasPrefix(res.Type, "aws:") && !strings.Contains(res.Provider, "pulumi:providers:aws::aws") {
				t.Errorf("%s %q uses provider %q", res.Type, res.Name, res.Provider)
			}
		}
		invokes := s.Invokes()
		if len(invokes) == 0 {
			t.Fatal("no invokes recorded; the zone, OIDC provider and key lookups are missing")
		}
		for _, invoke := range invokes {
			if strings.HasPrefix(invoke.Token, "aws:") && !strings.Contains(invoke.Provider, "pulumi:providers:aws::aws") {
				t.Errorf("invoke %s uses provider %q", invoke.Token, invoke.Provider)
			}
		}
	}
}

func TestTheProviderRefusesTheWrongAccountAndTagsEverything(t *testing.T) {
	provider := deployed(t, nil).Find(t, "pulumi:providers:aws", "aws")
	ids, _ := provider.Inputs["allowedAccountIds"].([]any)
	if !slices.Equal(ids, []any{infratest.AccountID}) {
		t.Errorf("allowedAccountIds = %v; credentials for any other account must be refused", ids)
	}
	tags := infratest.Get(provider.Inputs, "defaultTags", "tags")
	for key, want := range map[string]string{
		"app": slug, "environment": "dev", "managed-by": "pulumi", "pulumi-stack": "dev",
	} {
		if got := infratest.Get(tags, key); got != want {
			t.Errorf("default tag %s = %v, want %s", key, got, want)
		}
	}
	if got := provider.Inputs["region"]; got != infratest.Region {
		t.Errorf("region = %v", got)
	}
}

func TestEveryExpressServiceWaitsForTheInfrastructureRolesPolicy(t *testing.T) {
	// AWS documents the race: delete the policy before the service and the service sticks in
	// DRAINING. The dependency is what orders the destroy.
	for _, svc := range deployed(t, nil).OfType(serviceType) {
		if !slices.ContainsFunc(svc.Dependencies, func(urn string) bool {
			return strings.HasSuffix(urn, "aws:iam/rolePolicyAttachment:RolePolicyAttachment::infrastructure-managed")
		}) {
			t.Errorf("%s does not depend on the infrastructure role's policy attachment", svc.Name)
		}
		if !slices.Equal(svc.IgnoreChanges, []string{"primaryContainer.image"}) {
			t.Errorf("%s ignoreChanges = %v", svc.Name, svc.IgnoreChanges)
		}
	}
}

func TestTheBackendRunsAsItsOwnRoleInThePublicSubnets(t *testing.T) {
	s := deployed(t, nil)
	svc := s.Find(t, serviceType, slug)
	runtime, _ := s.Find(t, roleType, "runtime-role").Inputs["name"].(string)
	if got, _ := svc.Inputs["taskRoleArn"].(string); !strings.HasSuffix(got, "/"+runtime) {
		t.Errorf("taskRoleArn = %q, want the runtime role", got)
	}
	network := infratest.Get(svc.Inputs, "networkConfigurations", 0)
	subnets, _ := infratest.Get(network, "subnets").([]any)
	for _, subnet := range subnets {
		if !strings.HasPrefix(subnet.(string), "public-") {
			t.Errorf("the service runs in %v", subnet)
		}
	}
	if got := infratest.Get(network, "securityGroups", 0); got != "app-id" {
		t.Errorf("securityGroups = %v, want the app group", infratest.Get(network, "securityGroups"))
	}
	if svc.Inputs["cluster"] != slug+"-dev" {
		t.Errorf("cluster = %v", svc.Inputs["cluster"])
	}
}

func TestTheDeployRoleCanRollEveryServiceItDeploys(t *testing.T) {
	// The grant is attached after the services exist; without the service's ARN in it, cd.yml
	// fails on its first update with AccessDenied.
	s := deployed(t, nil)
	policy, _ := s.Find(t, "aws:iam/rolePolicy:RolePolicy", "deploy-permissions").Inputs["policy"].(string)
	if !strings.Contains(policy, s.exports["ecs_service_arn"]) {
		t.Errorf("the deploy policy does not name ecs_service_arn %q", s.exports["ecs_service_arn"])
	}
	for _, repository := range s.OfType("aws:ecr/repository:Repository") {
		if !strings.Contains(policy, ":repository/"+repository.Inputs["name"].(string)+`"`) {
			t.Errorf("the deploy policy cannot push to %v", repository.Inputs["name"])
		}
	}
}
