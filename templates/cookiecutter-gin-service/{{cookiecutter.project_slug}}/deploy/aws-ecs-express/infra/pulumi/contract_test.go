// The contract between the stack, the scripts around it, and the deploy pipelines.
//
// The failure this file exists to prevent: a stack exporting `deploy_role` and a pipeline reading
// `vars.AWS_DEPLOY_ROLE_ARN`. Nothing errors. The deploy job is simply skipped forever, because it
// is gated on a variable nothing ever sets.
package main

import (
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"{{ cookiecutter.go_module }}/infra/pulumi/internal/components"
)

const (
	infraDir     = ".."
	workflowsDir = "../../.github/workflows"
)

// manualVariables are repository variables a human sets by hand, because they come from outside
// this stack.
var manualVariables = []string{
	"SENTRY_ORG",
	"SENTRY_PROJECT",
}

// notSynced are exports no pipeline reads. Listed, so a new export that should have been synced
// fails a test instead of being silently left behind.
var notSynced = []string{
	// The application's AWS identity. It is where a grant goes, not something CI uses.
	"runtime_role_arn",
	// For a human with the console open.
	"rds_instance",
	// Read by sync-github.sh to check the trust policies, not copied into a variable.
	"github_subject_prefix",
	// Read by sync-github.sh to refuse un-gating CD too early, not copied into a variable.
	"secrets_ready",
}
{%- if cookiecutter.use_postgres == "yes" %}

// requiredSecretVersions are the stack configuration keys secrets.sh writes and secrets_ready
// waits for.
var requiredSecretVersions = []string{"jwtSecretVersion"}
{%- endif %}

var (
	mappingPattern  = regexp.MustCompile(`"([a-z_]+):([A-Z0-9_]+)"`)
	varsPattern     = regexp.MustCompile(`vars\.([A-Z0-9_]+)`)
	optionalPattern = regexp.MustCompile(`(?s)OPTIONAL=\((.*?)\)`)
)

func read(t *testing.T, path ...string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(path...))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func syncScript(t *testing.T) string {
	return read(t, infraDir, "scripts", "sync-github.sh")
}

// syncMapping is output -> variable, for every row in sync-github.sh.
func syncMapping(t *testing.T) map[string]string {
	t.Helper()
	mapping := map[string]string{}
	for _, match := range mappingPattern.FindAllStringSubmatch(syncScript(t), -1) {
		mapping[match[1]] = match[2]
	}
	if len(mapping) == 0 {
		t.Fatal("no output:VARIABLE rows found in sync-github.sh")
	}
	return mapping
}

func provided(t *testing.T) []string {
	return slices.Concat(slices.Collect(maps.Values(syncMapping(t))), manualVariables)
}

func referenced(workflow string) []string {
	var names []string
	for _, match := range varsPattern.FindAllStringSubmatch(workflow, -1) {
		if !slices.Contains(names, match[1]) {
			names = append(names, match[1])
		}
	}
	return names
}

func missingFrom(want, have []string) []string {
	var missing []string
	for _, name := range want {
		if !slices.Contains(have, name) {
			missing = append(missing, name)
		}
	}
	return missing
}

// --- Stack -> script -> pipeline --------------------------------------------------------------

func TestEveryOutputTheSyncScriptReadsIsOneTheStackExports(t *testing.T) {
	exports := slices.Collect(maps.Keys(deployed(t, nil).exports))
	if missing := missingFrom(slices.Collect(maps.Keys(syncMapping(t))), exports); missing != nil {
		t.Errorf("sync-github.sh reads outputs the stack never exports: %v", missing)
	}
}

func TestEveryExportIsSyncedOrDeliberatelyNot(t *testing.T) {
	mapping := syncMapping(t)
	for _, name := range allExports {
		_, synced := mapping[name]
		if synced == slices.Contains(notSynced, name) {
			t.Errorf("%s: synced=%v; either add it to sync-github.sh or to notSynced", name, synced)
		}
	}
}

func TestEveryVariableCDReadsIsOneTheSyncScriptSets(t *testing.T) {
	cd := read(t, workflowsDir, "cd.yml")
	if missing := missingFrom(referenced(cd), provided(t)); missing != nil {
		t.Errorf("cd.yml reads variables nothing sets: %v", missing)
	}
}

func TestEveryVariableInfraReadsIsOneTheSyncScriptSets(t *testing.T) {
	infra := read(t, workflowsDir, "infra.yml")
	if missing := missingFrom(referenced(infra), provided(t)); missing != nil {
		t.Errorf("infra.yml reads variables nothing sets: %v", missing)
	}
}

func TestAnOutputThatCanBeEmptyUnsetsItsVariableRatherThanFailingTheSync(t *testing.T) {
	// An empty output is the normal state for a feature that is off. The script must delete the
	// variable — so every step gated on it goes quiet — rather than refuse to run or leave a
	// stale value pointing at a resource that no longer exists.
	match := optionalPattern.FindStringSubmatch(syncScript(t))
	if match == nil {
		t.Fatal("sync-github.sh has no OPTIONAL=( ... ) list")
	}
	var optional []string
	for _, row := range mappingPattern.FindAllStringSubmatch(match[1], -1) {
		optional = append(optional, row[1])
	}
	for name, value := range deployed(t, nil).exports {
		if value == "" && !slices.Contains(notSynced, name) && !slices.Contains(optional, name) {
			t.Errorf("%s can be empty but sync-github.sh treats it as required", name)
		}
	}
}

func TestTheSyncScriptSetsNoSecrets(t *testing.T) {
	// Keyless: every value is a public identifier. A `gh secret set` here is a credential that
	// should not exist.
	if strings.Contains(syncScript(t), "gh secret set") {
		t.Error("sync-github.sh sets a GitHub secret")
	}
}

func TestTheSyncScriptRefusesToUngateCDBeforeTheSecretsAreReady(t *testing.T) {
	// Un-gating CD deploys an image that refuses to start without its signing key. The check has
	// to come before the first variable is set — WIF/the role ARN among them — and has to name the
	// fix.
	script := syncScript(t)
	check := strings.Index(script, "read_output secrets_ready")
	firstSet := strings.Index(script, "gh variable set")
	if check < 0 || firstSet < 0 || check > firstSet {
		t.Error("sync-github.sh must check secrets_ready before it sets any variable")
	}
	if !strings.Contains(script, `!= "true"`) || !strings.Contains(script, "just infra-secrets") {
		t.Error("sync-github.sh must refuse unless secrets_ready is true, and print `just infra-secrets` as the fix")
	}
}
{%- if cookiecutter.use_postgres == "yes" %}

func secretsScript(t *testing.T) string {
	return read(t, infraDir, "scripts", "secrets.sh")
}

func TestTheSecretsRecipeRunsTheSecretsScript(t *testing.T) {
	if line := lineContaining(read(t, infraDir, "justfile"), "./scripts/secrets.sh"); line == "" {
		t.Error("infra/justfile has no recipe running scripts/secrets.sh")
	}
}

func TestTheSecretsScriptRecordsEveryVersionTheStackReads(t *testing.T) {
	// The script writes the version numbers the stack mounts. A key spelled differently on the two
	// sides is a secret that is generated, stored, and never mounted.
	script := secretsScript(t)
	for _, key := range requiredSecretVersions {
		if !strings.Contains(script, "config set "+key) && !strings.Contains(script, "record_version "+key) {
			t.Errorf("secrets.sh never sets %s", key)
		}
	}
}

func TestTheSecretsScriptNeverPutsAValueWhereItCanBeSeen(t *testing.T) {
	// Generated, piped and forgotten: no file, no argument a process listing shows, no echo, no
	// trace. And no silent replacement — a version already set is kept unless --rotate says so.
	script := secretsScript(t)
	for _, forbidden := range []string{"set -x", "--password", "mktemp", "> /tmp", "tee "} {
		for _, line := range strings.Split(script, "\n") {
			if !strings.HasPrefix(strings.TrimSpace(line), "#") && strings.Contains(line, forbidden) {
				t.Errorf("secrets.sh contains %q: %s", forbidden, strings.TrimSpace(line))
			}
		}
	}
	for _, want := range []string{"set -euo pipefail", "openssl rand -base64 48", "--rotate"} {
		if !strings.Contains(script, want) {
			t.Errorf("secrets.sh lacks %q", want)
		}
	}
}
{%- endif %}

// --- The pipelines ----------------------------------------------------------------------------

func deployWorkflows(t *testing.T) map[string]string {
	t.Helper()
	return map[string]string{"cd.yml": read(t, workflowsDir, "cd.yml")}
}

func TestEveryDeployStaysInertUntilItIsConfigured(t *testing.T) {
	// A committed deploy path that cannot fire by accident is reviewed alongside the code it
	// ships, rather than written under pressure on the day.
	for name, workflow := range deployWorkflows(t) {
		if !strings.Contains(workflow, "if: vars.AWS_DEPLOY_ROLE_ARN != ''") {
			t.Errorf("%s is not gated on AWS_DEPLOY_ROLE_ARN", name)
		}
	}
	infra := read(t, workflowsDir, "infra.yml")
	if !strings.Contains(infra, "vars.AWS_PREVIEW_ROLE_ARN != ''") {
		t.Error("infra.yml's preview is not gated on AWS_PREVIEW_ROLE_ARN")
	}
	if strings.Contains(infra, "AWS_DEPLOY_ROLE_ARN") {
		t.Error("infra.yml uses the deploy role; a pull request gets the read-only preview role")
	}
}

func TestTheDeployJobRunsInTheEnvironmentTheDeployRoleTrusts(t *testing.T) {
	// The deploy role's trust policy names one GitHub environment. A job in any other — or in
	// none — gets a token with a different `sub`, and AssumeRoleWithWebIdentity refuses it.
	if !strings.Contains(read(t, workflowsDir, "cd.yml"), "environment: "+components.DeployEnvironment) {
		t.Errorf("cd.yml does not run in the %q environment", components.DeployEnvironment)
	}
}

func TestTheMigrationStepIsSkippedWithoutATaskFamily(t *testing.T) {
	if !strings.Contains(read(t, workflowsDir, "cd.yml"), "if: vars.ECS_MIGRATE_TASK_FAMILY != ''") {
		t.Error("cd.yml must skip the migrate step when there is no migration task")
	}
}

func TestEveryWorkflowIsFilteredToWhatItBuilds(t *testing.T) {
	// Without a filter, a typo fix in a README rebuilds and redeploys a container.
	for _, name := range []string{"cd.yml", "infra.yml"} {
		if !strings.Contains(read(t, workflowsDir, name), "paths:") {
			t.Errorf("%s has no paths: filter", name)
		}
	}
}

func TestEveryDeployTagsTheImageWithTheCommitRatherThanLatest(t *testing.T) {
	// A rollback has to be able to name an image. :latest is a guess about what it pointed at
	// last Tuesday — and the repository is IMMUTABLE, so pushing it twice fails anyway.
	for name, workflow := range deployWorkflows(t) {
		var code []string
		for _, line := range strings.Split(workflow, "\n") {
			if !strings.HasPrefix(strings.TrimSpace(line), "#") {
				code = append(code, line)
			}
		}
		if strings.Contains(strings.Join(code, "\n"), ":latest") {
			t.Errorf("%s uses :latest", name)
		}
		if !strings.Contains(workflow, "github.sha") {
			t.Errorf("%s does not tag with github.sha", name)
		}
	}
}

func TestTheDeployUpdatesTheImageRatherThanRecreatingTheService(t *testing.T) {
	// The stack owns the service's shape. The pipeline reads the live primary container, changes
	// the image and sends it back; it never creates, deletes or reshapes the service.
	cd := read(t, workflowsDir, "cd.yml")
	for _, forbidden := range []string{"create-express-gateway-service", "delete-express-gateway-service", "pulumi up"} {
		if strings.Contains(cd, forbidden) {
			t.Errorf("cd.yml runs %s", forbidden)
		}
	}
	for _, want := range []string{"describe-express-gateway-service", "update-express-gateway-service", "rolloutState"} {
		if !strings.Contains(cd, want) {
			t.Errorf("cd.yml does not run %s", want)
		}
	}
	// `wait services-stable` gives up after ten minutes, and a healthy Express canary has taken
	// longer than that: the job reports a failed deploy that succeeded.
	if strings.Contains(cd, "wait services-stable") {
		t.Error("cd.yml waits with services-stable, which times out before an Express rollout finishes")
	}
}

func TestTheMigrationFailsTheDeployOnANonZeroExit(t *testing.T) {
	// `run-task` returns as soon as the task is placed. A pipeline that does not wait for it, and
	// read its exit code, rolls the service onto a schema that was never migrated.
	cd := read(t, workflowsDir, "cd.yml")
	for _, want := range []string{
		"wait tasks-stopped", "exitCode", "register-task-definition", "assignPublicIp=ENABLED",
		// The task definition has one container and the step rewrites it by position.
		".containerDefinitions[0].image",
	} {
		if !strings.Contains(cd, want) {
			t.Errorf("cd.yml's migrate step lacks %s", want)
		}
	}
}

func TestNoWorkflowCarriesACookiecutterVariable(t *testing.T) {
	// .github/workflows/ is _copy_without_render, because a GitHub Actions expression and a Jinja
	// one are written with the same two braces. A cookiecutter variable in one of these files is
	// therefore never substituted: it survives into the generated repository as literal text.
	//
	// The patterns put the second character in a class so that this file — which IS rendered —
	// contains no Jinja delimiter of its own.
	jinja := regexp.MustCompile(`\{[{]\s*cookiecutter|\{[%#]`)
	for _, name := range []string{"cd.yml", "infra.yml"} {
		if jinja.MatchString(read(t, workflowsDir, name)) {
			t.Errorf("%s contains Jinja", name)
		}
	}
}

// --- Bootstrap and teardown -------------------------------------------------------------------

func TestTheBootstrapScriptCreatesWhatTheStackLooksUp(t *testing.T) {
	// The stack looks up the state key by alias and the OIDC provider by URL, and scopes the
	// preview role to the bucket by name. If any of the three drifts from what state-bucket.sh
	// creates, the first preview fails on a resource that does not exist.
	for _, script := range []string{"state-bucket.sh", "teardown.sh"} {
		source := read(t, infraDir, "bootstrap", script)
		for _, want := range []string{
			`BUCKET="pulumi-state-${ACCOUNT}-${REGION}"`,
			`KEY_ALIAS="` + components.StateKeyAlias + `"`,
			components.GitHubOIDCHost,
		} {
			if !strings.Contains(source, want) {
				t.Errorf("%s does not contain %s", script, want)
			}
		}
	}
	bootstrap := read(t, infraDir, "bootstrap", "state-bucket.sh")
	for _, want := range []string{components.GitHubAudience, "enable-key-rotation", "BucketKeyEnabled", "aws:SecureTransport"} {
		if !strings.Contains(bootstrap, want) {
			t.Errorf("state-bucket.sh does not contain %s", want)
		}
	}
}

func TestBothScriptsRefuseTheWrongAccount(t *testing.T) {
	// The bucket name and the key are derived from the account id. Credentials for another
	// account would create — or delete — a backend in an account nobody meant.
	for _, script := range []string{"state-bucket.sh", "teardown.sh"} {
		source := read(t, infraDir, "bootstrap", script)
		if !strings.Contains(source, "sts get-caller-identity") || !strings.Contains(source, `!= "${ACCOUNT}"`) {
			t.Errorf("%s does not check the caller's account", script)
		}
	}
}

func TestTheTeardownScriptCannotStrandAStack(t *testing.T) {
	// Deleting the state bucket while any stack still holds resources leaves those resources
	// running, billing, and with no record of what they are.
	script := read(t, infraDir, "bootstrap", "teardown.sh")
	for want, why := range map[string]string{
		"stack export":              "the emptiness check is the guard; do not remove it",
		"stack ls --all":            "without --all, another Pulumi project's live stack is absent rather than under-counted",
		"! -t 0":                    "it must refuse when nothing can confirm",
		`"${reply}" != "${BUCKET}"`: "it must make a human type the bucket name",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("teardown.sh lacks %q: %s", want, why)
		}
	}
}

func TestTheTeardownScriptCannotDeleteABackendItShares(t *testing.T) {
	// Pulumi standards MUST-8. The bucket is named for the account and region alone, so a second
	// service in the same account bootstraps onto the same bucket. Ownership is established by
	// listing the backend's project directories, before anything is deleted.
	script := read(t, infraDir, "bootstrap", "teardown.sh")
	listing := strings.Index(script, ".pulumi/stacks/")
	deletion := strings.Index(script, "s3api delete-bucket ")
	if listing < 0 || deletion < 0 || listing > deletion {
		t.Error("the backend listing must happen before anything is deleted")
	}
	if !strings.Contains(script, "PULUMI_PROJECT") {
		t.Error("the listing has to be compared against this project's name")
	}
}

func TestTheTeardownScriptKeepsAnOIDCProviderSomeRoleStillTrusts(t *testing.T) {
	// The provider is one per account. Deleting it while another service's CI role trusts it
	// breaks that service's next deploy, in a repository this one cannot see.
	script := read(t, infraDir, "bootstrap", "teardown.sh")
	check := strings.Index(script, "iam list-roles")
	deletion := strings.Index(script, "delete-open-id-connect-provider")
	if check < 0 || deletion < 0 || check > deletion {
		t.Error("teardown.sh must look for roles trusting the OIDC provider before deleting it")
	}
}

func TestTheRecipesRefreshAndNeverApproveForYou(t *testing.T) {
	// --refresh is load-bearing: CD changes the image out of band, and without a refresh the next
	// apply rolls the service back to it. --yes is the human's to type.
	justfile := read(t, infraDir, "justfile")
	for _, command := range []string{"pulumi preview", "pulumi up", "pulumi destroy"} {
		line := lineContaining(justfile, command)
		if !strings.Contains(line, "--refresh") {
			t.Errorf("%q does not pass --refresh: %s", command, line)
		}
	}
	if line := lineContaining(justfile, "--yes"); line != "" {
		t.Errorf("a recipe passes --yes; the person running it types that: %s", line)
	}
}

func lineContaining(text, needle string) string {
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, needle) && !strings.HasPrefix(strings.TrimSpace(line), "#") {
			return line
		}
	}
	return ""
}
