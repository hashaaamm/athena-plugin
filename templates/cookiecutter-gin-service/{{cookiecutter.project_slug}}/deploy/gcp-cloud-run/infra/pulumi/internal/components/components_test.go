// Component tests. No cloud credentials, no network, no state.
//
// What is worth testing here is not "does Pulumi work" but the handful of settings where a silent
// mistake is expensive: a Workload Identity pool any repository can use, a service running as the
// default compute account, a secret pinned to `latest`, or a probe pointed at a path that is never
// going to answer.
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
	slug         = "{{ cookiecutter.project_slug }}"
	repository   = "{{ cookiecutter.github_repository }}"
	runtimeEmail = "runtime@test-project.iam.gserviceaccount.com"

	serviceType = "gcp:cloudrunv2/service:Service"
	jobType     = "gcp:cloudrunv2/job:Job"
)

var cfg = stackconfig.Defaults(infratest.Project, "europe-west1", "dev", repository, slug)

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
		App:          slug,
		RuntimeEmail: pulumi.String(runtimeEmail),
		Port:         8080,
		HealthPath:   "/health/live",
		Env:          map[string]pulumi.StringInput{"ENVIRONMENT": pulumi.String("production")},
	}
}

func container(t *testing.T, rec *infratest.Recorder, app string) any {
	t.Helper()
	svc := rec.Find(t, serviceType, app)
	c := infratest.Get(svc.Inputs, "template", "containers", 0)
	if c == nil {
		t.Fatalf("%s has no container", app)
	}
	return c
}

// --- Security ---------------------------------------------------------------------------------

func TestWorkloadIdentityIsScopedToOneRepository(t *testing.T) {
	// Without the attribute condition, any repository on GitHub can assume this identity.
	rec := run(t, func(ctx *pulumi.Context) error {
		_, err := components.NewIdentities(ctx, cfg, components.IdentitiesArgs{
			ProjectNumber: pulumi.String(infratest.ProjectNumber),
		})
		return err
	})
	provider := rec.Find(t, "gcp:iam/workloadIdentityPoolProvider:WorkloadIdentityPoolProvider", "github-provider")
	want := `assertion.repository == "` + repository + `"`
	if got := provider.Inputs["attributeCondition"]; got != want {
		t.Errorf("attributeCondition = %v, want %s", got, want)
	}
	if got := infratest.Get(provider.Inputs, "oidc", "issuerUri"); got != components.GitHubIssuer {
		t.Errorf("issuer = %v", got)
	}

	// And only that repository's principal set may impersonate the CI account.
	binding := rec.Find(t, "gcp:serviceaccount/iAMMember:IAMMember", "github-impersonates-ci")
	member, _ := binding.Inputs["member"].(string)
	if !strings.HasSuffix(member, "/attribute.repository/"+repository) ||
		!strings.Contains(member, "/projects/"+infratest.ProjectNumber+"/") {
		t.Errorf("workloadIdentityUser member = %q", member)
	}
}

func TestCIHoldsNoProjectWideWriteOrSecretReadRole(t *testing.T) {
	// CI can deploy and push images. It must not be able to read a secret's value or rewrite state
	// outside the one bucket it needs — both are granted per resource, not per project.
	forbidden := []string{
		"roles/owner", "roles/editor",
		"roles/storage.admin", "roles/storage.objectAdmin", "roles/storage.objectUser",
		"roles/secretmanager.secretAccessor", "roles/secretmanager.admin",
		"roles/iam.serviceAccountUser",
	}
	for _, role := range components.CIRoles {
		if slices.Contains(forbidden, role) {
			t.Errorf("%s is project-wide; it belongs scoped to one resource", role)
		}
	}
	// The pull-request preview runs with --refresh, which reads every resource and every IAM
	// binding the stack declares. viewer covers resource state; it excludes getIamPolicy.
	for _, role := range []string{"roles/viewer", "roles/iam.securityReviewer"} {
		if !slices.Contains(components.CIRoles, role) {
			t.Errorf("CI needs %s for the pull-request preview", role)
		}
	}
}

func TestTheRuntimeAccountCannotDeploy(t *testing.T) {
	for _, role := range []string{"roles/run.developer", "roles/run.admin", "roles/artifactregistry.writer"} {
		if slices.Contains(components.RuntimeRoles, role) {
			t.Errorf("the runtime account holds %s", role)
		}
	}
}

func TestTheServiceRunsAsTheRuntimeAccountNotTheDefault(t *testing.T) {
	// The default compute account holds Editor on the whole project.
	rec := newService(t, backendArgs())
	svc := rec.Find(t, serviceType, slug)
	if got := infratest.Get(svc.Inputs, "template", "serviceAccount"); got != runtimeEmail {
		t.Errorf("serviceAccount = %v", got)
	}
}

func TestAServiceRefusesASecretItWasNeverGiven(t *testing.T) {
	// Otherwise the failure is a revision that cannot resolve its secret, which reads as a broken
	// application rather than a mistyped key.
	args := backendArgs()
	args.SecretEnv = map[string]string{"JWT_SECRET": "typo"}
	args.SecretRefs = map[string]components.SecretRef{
		"jwt-secret": {Secret: pulumi.String("x"), Version: pulumi.String("1")},
	}
	_, err := infratest.TryRun(slug, nil, func(ctx *pulumi.Context) error {
		_, err := components.NewService(ctx, cfg, args)
		return err
	})
	if err == nil || !strings.Contains(err.Error(), `"typo"`) {
		t.Fatalf("err = %v, want one naming the missing secret", err)
	}
}

func TestAMountedSecretIsPinnedToAVersionNumber(t *testing.T) {
	// Cloud Run reads an environment secret when an instance starts, so `latest` lets the
	// instances of one revision hold different values after a rotation. Pinned, a rotation is a
	// new revision and a rollback restores the credential with the image.
	args := backendArgs()
	args.SecretEnv = map[string]string{"JWT_SECRET": "jwt-secret"}
	args.SecretRefs = map[string]components.SecretRef{
		"jwt-secret": {Secret: pulumi.String("jwt-secret-dev"), Version: pulumi.String("7")},
	}
	env := infratest.EnvOf(container(t, newService(t, args), slug))
	if got := env["JWT_SECRET"]; got != "secret:jwt-secret-dev@7" {
		t.Errorf("JWT_SECRET = %q", got)
	}
}

// readers is every secret the stack creates, read by the runtime account.
func readers() map[string]pulumi.StringInput {
	out := map[string]pulumi.StringInput{}
	for _, name := range components.SecretNames {
		out[name] = pulumi.String(runtimeEmail)
	}
	return out
}

func TestTheStackCreatesEverySecretEmptyAndWritesNoValue(t *testing.T) {
	// Pulumi standards MUST-3: a secret value is never in the program, its configuration or its
	// state. So the stack makes containers and grants and never a version — not even of a value it
	// could have generated, because a generated value is in state like any other.
	var secrets *components.Secrets
	rec := run(t, func(ctx *pulumi.Context) error {
		var err error
		secrets, err = components.NewSecrets(ctx, cfg, components.SecretsArgs{Readers: readers()})
		return err
	})
	if versions := rec.OfType("gcp:secretmanager/secretVersion:SecretVersion"); len(versions) != 0 {
		t.Errorf("the stack writes %d secret versions", len(versions))
	}
	if len(secrets.IDs) != len(components.SecretNames) {
		t.Errorf("%d containers for %d secret names", len(secrets.IDs), len(components.SecretNames))
	}
	if len(secrets.Refs) != 0 {
		t.Errorf("references %v before any version number is configured", secrets.Refs)
	}
	// Access is granted per secret, never project-wide.
	if got, want := len(rec.OfType("gcp:secretmanager/secretIamMember:SecretIamMember")), len(secrets.IDs); got != want {
		t.Errorf("%d per-secret grants for %d secrets", got, want)
	}
}

func TestASecretIsReadByTheAccountItIsGivenToAndNoOther(t *testing.T) {
	if len(components.SecretNames) == 0 {
		t.Skip("this service has no secrets")
	}
	given := readers()
	name := components.SecretNames[0]
	given[name] = pulumi.String("someone-else@test-project.iam.gserviceaccount.com")
	rec := run(t, func(ctx *pulumi.Context) error {
		_, err := components.NewSecrets(ctx, cfg, components.SecretsArgs{Readers: given})
		return err
	})
	grant := rec.Find(t, "gcp:secretmanager/secretIamMember:SecretIamMember", name+"-accessor")
	if got := grant.Inputs["member"]; got != "serviceAccount:someone-else@test-project.iam.gserviceaccount.com" {
		t.Errorf("%s is readable by %v", name, got)
	}
	// And a secret nobody was named to read is refused at plan time.
	delete(given, name)
	if _, err := infratest.TryRun(slug, nil, func(ctx *pulumi.Context) error {
		_, err := components.NewSecrets(ctx, cfg, components.SecretsArgs{Readers: given})
		return err
	}); err == nil || !strings.Contains(err.Error(), "no reader") {
		t.Errorf("err = %v; want a refusal naming the readerless secret", err)
	}
}

func TestAConfiguredVersionIsPinnedAndAnEmptyOneIsNotMounted(t *testing.T) {
	if len(components.SecretNames) == 0 {
		t.Skip("this service has no secrets")
	}
	name := components.SecretNames[0]
	var secrets *components.Secrets
	run(t, func(ctx *pulumi.Context) error {
		var err error
		secrets, err = components.NewSecrets(ctx, cfg, components.SecretsArgs{
			Readers:  readers(),
			Versions: map[string]string{name: "4"},
		})
		return err
	})
	ref, ok := secrets.Refs[name]
	if !ok {
		t.Fatalf("no reference for %s at version 4", name)
	}
	if got, ok := ref.Version.(pulumi.String); !ok || got != "4" {
		t.Errorf("version = %v, want 4", ref.Version)
	}
	if len(secrets.Refs) != 1 {
		t.Errorf("references %v; only the configured one may be mounted", secrets.Refs)
	}
}

// --- Cloud Run shape --------------------------------------------------------------------------

func TestTheStartupProbeReadsLivenessAndTheContainerPort(t *testing.T) {
	// A startup probe pointed at readiness never lets a first revision up, because readiness can
	// legitimately be false until something else has run.
	c := container(t, newService(t, backendArgs()), slug)
	if got := infratest.Get(c, "startupProbe", "httpGet", "path"); got != "/health/live" {
		t.Errorf("probe path = %v", got)
	}
	if got := infratest.Get(c, "startupProbe", "httpGet", "port"); got != 8080.0 {
		t.Errorf("probe port = %v", got)
	}
	if got := infratest.Get(c, "ports", "containerPort"); got != 8080.0 {
		t.Errorf("container port = %v", got)
	}
}

func TestIdleCPUThrottlingComesWithTheStartupBoost(t *testing.T) {
	// Without the boost, throttling applies during startup too and a cold start does not finish
	// inside the probe budget. The symptom is an intermittently failing deploy.
	c := container(t, newService(t, backendArgs()), slug)
	if infratest.Get(c, "resources", "cpuIdle") != true || infratest.Get(c, "resources", "startupCpuBoost") != true {
		t.Errorf("resources = %v", infratest.Get(c, "resources"))
	}
}

func TestTheServiceMountsNoVolume(t *testing.T) {
	// A service with a database reaches it through the Go connector in its own process; a Cloud SQL
	// volume would be a second way in that it never uses.
	rec := newService(t, backendArgs())
	svc := rec.Find(t, serviceType, slug)
	if volumes, _ := infratest.Get(svc.Inputs, "template", "volumes").([]any); len(volumes) != 0 {
		t.Errorf("volumes = %v", volumes)
	}
	if mounts, _ := infratest.Get(container(t, rec, slug), "volumeMounts").([]any); len(mounts) != 0 {
		t.Errorf("volumeMounts = %v", mounts)
	}
}

func TestTheEnvironmentIsInTheSameOrderEveryRun(t *testing.T) {
	// A map has no order. A container whose environment comes out shuffled is a diff, and a new
	// revision, for nothing.
	args := backendArgs()
	args.Env = map[string]pulumi.StringInput{}
	for _, name := range []string{"ZULU", "ALPHA", "MIKE", "BRAVO", "YANKEE"} {
		args.Env[name] = pulumi.String(name)
	}
	envs, _ := infratest.Get(container(t, newService(t, args), slug), "envs").([]any)
	var names []string
	for _, env := range envs {
		names = append(names, infratest.Get(env, "name").(string))
	}
	if !slices.IsSorted(names) {
		t.Errorf("env order = %v", names)
	}
}

func TestThePipelineOwnsTheImageAndNothingElseDoes(t *testing.T) {
	// Two tools writing one field is how an infrastructure repository stops being trusted.
	if components.ServiceIgnoreChanges[0] != "template.containers[0].image" {
		t.Errorf("service ignore_changes = %v", components.ServiceIgnoreChanges)
	}
	if components.JobIgnoreChanges[0] != "template.template.containers[0].image" {
		t.Errorf("job ignore_changes = %v", components.JobIgnoreChanges)
	}
	c := container(t, newService(t, backendArgs()), slug)
	if got := infratest.Get(c, "image"); got != components.BootstrapImage {
		t.Errorf("image = %v; the stack declares the placeholder and CD replaces it", got)
	}
}

func TestEveryJobRunsTheSameImageAsItsServiceWithItsOwnCommand(t *testing.T) {
	report := components.JobSpec{Name: "report", Command: []string{"/app/api"}, Args: []string{"report"}}
	args := backendArgs()
	args.Jobs = []components.JobSpec{report}
	rec := newService(t, args)
	job := rec.Find(t, jobType, slug+"-report")
	c := infratest.Get(job.Inputs, "template", "template", "containers", 0)
	if got := infratest.Get(c, "commands", 0); got != "/app/api" {
		t.Errorf("command = %v", got)
	}
	// One attempt: a failed run needs a human reading the log, not a retry that fails identically.
	if got := infratest.Get(job.Inputs, "template", "template", "maxRetries"); got != 0.0 {
		t.Errorf("maxRetries = %v", got)
	}
	// The service's account, unless the job is given one of its own; no database, no volume.
	if got := infratest.Get(job.Inputs, "template", "template", "serviceAccount"); got != runtimeEmail {
		t.Errorf("serviceAccount = %v", got)
	}
	if volumes, _ := infratest.Get(job.Inputs, "template", "template", "volumes").([]any); len(volumes) != 0 {
		t.Errorf("volumes = %v", volumes)
	}
}

func TestAJobWithADatabaseMountsItsConnectionAndRunsAsItsOwnAccount(t *testing.T) {
	migrate := components.JobSpec{
		Name:             "migrate",
		Command:          []string{"/app/api"},
		ServiceAccount:   pulumi.String("migrator@test-project.iam.gserviceaccount.com"),
		CloudSQLInstance: pulumi.String("test-project:europe-west1:pg"),
	}
	args := backendArgs()
	args.Jobs = []components.JobSpec{migrate}
	job := newService(t, args).Find(t, jobType, slug+"-migrate")
	template := infratest.Get(job.Inputs, "template", "template")
	if got := infratest.Get(template, "serviceAccount"); got != "migrator@test-project.iam.gserviceaccount.com" {
		t.Errorf("serviceAccount = %v", got)
	}
	if got := infratest.Get(template, "volumes", 0, "cloudSqlInstance", "instances", 0); got != "test-project:europe-west1:pg" {
		t.Errorf("Cloud SQL volume = %v", got)
	}
	if got := infratest.Get(template, "containers", 0, "volumeMounts", 0, "mountPath"); got != "/cloudsql" {
		t.Errorf("mount = %v", got)
	}
}

// --- APIs -------------------------------------------------------------------------------------

func TestEveryServiceTheStackUsesHasItsAPIEnabledOnce(t *testing.T) {
	// A missing API surfaces as a mid-apply failure with half the stack created; an API enabled
	// from two places is a race on a fresh project and a confusing diff after.
	enabled := slices.Concat(components.BootstrapAPIs, components.RequiredAPIs)
	for _, api := range []string{
		"run.googleapis.com", "artifactregistry.googleapis.com", "secretmanager.googleapis.com",
		"iam.googleapis.com", "iamcredentials.googleapis.com", "sts.googleapis.com",
		"compute.googleapis.com", "cloudresourcemanager.googleapis.com", "serviceusage.googleapis.com",
	} {
		if !slices.Contains(enabled, api) {
			t.Errorf("%s is never enabled", api)
		}
	}
	for _, api := range components.BootstrapAPIs {
		if slices.Contains(components.RequiredAPIs, api) {
			t.Errorf("%s is enabled by both the bootstrap script and the stack", api)
		}
	}
}
