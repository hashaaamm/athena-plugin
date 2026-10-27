// The contract between the stack, the scripts around it, and the deploy pipelines.
//
// The failure this file exists to prevent: a stack exporting `workload_identity_provider` and a
// pipeline reading `vars.WIF_PROVIDER`. Nothing errors. The deploy job is simply skipped forever,
// because it is gated on a variable nothing ever sets.
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
var notSynced = []string{"cloud_sql_instance"}

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

// --- The pipelines ----------------------------------------------------------------------------

func deployWorkflows(t *testing.T) map[string]string {
	t.Helper()
	return map[string]string{"cd.yml": read(t, workflowsDir, "cd.yml")}
}

func TestEveryDeployStaysInertUntilItIsConfigured(t *testing.T) {
	// A committed deploy path that cannot fire by accident is reviewed alongside the code it
	// ships, rather than written under pressure on the day.
	for name, workflow := range deployWorkflows(t) {
		if !strings.Contains(workflow, "if: vars.WIF_PROVIDER != ''") {
			t.Errorf("%s is not gated on WIF_PROVIDER", name)
		}
	}
	if !strings.Contains(read(t, workflowsDir, "infra.yml"), "vars.WIF_PROVIDER != ''") {
		t.Error("infra.yml's preview is not gated on WIF_PROVIDER")
	}
}

func TestTheMigrationStepIsSkippedWithoutAJob(t *testing.T) {
	if !strings.Contains(read(t, workflowsDir, "cd.yml"), "if: vars.CLOUD_RUN_MIGRATE_JOB != ''") {
		t.Error("cd.yml must skip the migrate step when there is no migration job")
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
	// last Tuesday, and two pipelines racing on it is a deploy nobody can reconstruct.
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

func TestTheDeployUpdatesTheImageRatherThanRedeployingTheService(t *testing.T) {
	// `gcloud run deploy` rewrites the whole service from flags and fights the stack over every
	// field it owns. `update --image` changes the one field CD owns.
	cd := read(t, workflowsDir, "cd.yml")
	if strings.Contains(cd, "gcloud run deploy") || !strings.Contains(cd, "gcloud run services update") {
		t.Error("cd.yml must `gcloud run services update --image`, never `gcloud run deploy`")
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

func TestTheBootstrapScriptEnablesWhatTheStackCannot(t *testing.T) {
	// Enabling an API is itself a serviceusage call authorised through cloudresourcemanager, and
	// the provider reads the default region and zone from compute before planning anything. If one
	// of those drifts out of the bootstrap script, the first `pulumi up` on a fresh project fails
	// with SERVICE_DISABLED and no useful resource to point at.
	script := read(t, infraDir, "bootstrap", "state-bucket.sh")
	for _, api := range components.BootstrapAPIs {
		if !strings.Contains(script, api) {
			t.Errorf("state-bucket.sh does not enable %s", api)
		}
	}
}

func TestTheTeardownScriptNamesWhatTheBootstrapScriptCreated(t *testing.T) {
	// `pulumi destroy` cannot reach the state bucket or the KMS key — the stack never created
	// them — so teardown.sh is the only thing that ever removes them. If its names drift from the
	// ones state-bucket.sh created, it deletes nothing, exits 0, and the resources keep billing.
	for _, script := range []string{"state-bucket.sh", "teardown.sh"} {
		source := read(t, infraDir, "bootstrap", script)
		for _, want := range []string{
			components.StateBucketSuffix,
			`KEYRING="` + components.Keyring + `"`,
			`KEY="` + components.Key + `"`,
		} {
			if !strings.Contains(source, want) {
				t.Errorf("%s does not contain %s", script, want)
			}
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
	// Pulumi standards MUST-8. state-bucket.sh derives the bucket from the GCP project alone, so a
	// second service generated into the same project bootstraps onto the same bucket. Ownership
	// is established by listing the backend's project directories, before anything is deleted.
	script := read(t, infraDir, "bootstrap", "teardown.sh")
	listing := strings.Index(script, "/.pulumi/stacks/")
	deletion := strings.Index(script, "gcloud storage rm")
	if listing < 0 || deletion < 0 || listing > deletion {
		t.Error("the backend listing must happen before anything is deleted")
	}
	if !strings.Contains(script, "PULUMI_PROJECT") {
		t.Error("the listing has to be compared against this project's name")
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
