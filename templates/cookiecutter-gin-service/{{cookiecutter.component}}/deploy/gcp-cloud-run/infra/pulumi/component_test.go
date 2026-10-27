// The component's place in its repository (ADR-0024): one folder of a repository that may hold
// several, whose workflows live at the repository root under the component's name and whose
// repository variables carry its prefix. Two components side by side must need no hand edit, and
// these tests are what holds every name to that.
package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	// component is this component's folder, and the prefix of its workflow files.
	component = "{{ cookiecutter.component }}"
	// varPrefix starts every repository variable and secret this component reads or writes.
	varPrefix = "{{ cookiecutter.__component_var }}_"
)

// workflowNames are the component's workflows, as the template ships them. Each is installed at
// the repository root as .github/workflows/<component>-<name>.
var workflowNames = []string{"ci.yml", "cd.yml", "infra.yml"}

var (
	secretsPattern = regexp.MustCompile(`secrets\.([A-Za-z0-9_]+)`)
	// Spelled with a character class so that this file, which is rendered, holds no token itself.
	tokenPattern = regexp.MustCompile(`_[_]COMPONENT`)
)

// repositoryRoot is the nearest directory above this one that holds .github: the repository this
// component was generated into.
func repositoryRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	for {
		if info, err := os.Stat(filepath.Join(dir, ".github")); err == nil && info.IsDir() {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no .github directory above this one: the component's workflows were never installed")
		}
		dir = parent
	}
}

// workflow reads one of this component's workflows from the repository root.
func workflow(t *testing.T, name string) string {
	t.Helper()
	return read(t, repositoryRoot(t), ".github", "workflows", component+"-"+name)
}

func firstDeployScript(t *testing.T) string {
	return read(t, infraDir, "scripts", "first-deploy.sh")
}

// --- The workflows, at the root ---------------------------------------------------------------

func TestNoTokenSurvivesGeneration(t *testing.T) {
	// The post-generation hook substitutes the component's name for two tokens. One left behind is
	// a variable named after a token, which no script ever sets.
	for _, name := range workflowNames {
		if tokenPattern.MatchString(workflow(t, name)) {
			t.Errorf("%s-%s still holds a component token", component, name)
		}
	}
}

func TestEveryWorkflowIsNamedForTheComponent(t *testing.T) {
	// Two components' workflows side by side in the Actions tab, and their concurrency groups in
	// one repository, must not be mistaken for — or cancel — each other.
	for _, name := range workflowNames {
		text := workflow(t, name)
		if !regexp.MustCompile(`(?m)^name: ` + regexp.QuoteMeta(component) + ` `).MatchString(text) {
			t.Errorf("%s's name does not start with %q", name, component)
		}
		if !regexp.MustCompile(`(?m)^\s+group: ` + regexp.QuoteMeta(component) + `-`).MatchString(text) {
			t.Errorf("%s's concurrency group does not start with %q", name, component+"-")
		}
		if !strings.Contains(text, "working-directory: "+component) {
			t.Errorf("%s does not run in the component's folder", name)
		}
	}
}

func TestEveryWorkflowIsFilteredToItsOwnComponent(t *testing.T) {
	// A change to another component's folder must not run this component's pipelines, and a change
	// to this workflow must run it.
	for _, name := range workflowNames {
		text := workflow(t, name)
		if !strings.Contains(text, "'.github/workflows/"+component+"-"+name+"'") {
			t.Errorf("%s's path filter does not name the workflow itself", name)
		}
		for _, line := range regexp.MustCompile(`(?m)^\s+- '([^']+)'$`).FindAllStringSubmatch(text, -1) {
			path := line[1]
			if !strings.HasPrefix(path, component+"/") && !strings.HasPrefix(path, ".github/workflows/"+component+"-") {
				t.Errorf("%s filters on %q, outside the component", name, path)
			}
		}
	}
}

func TestEveryJobChecksOutBeforeAnythingElse(t *testing.T) {
	// The default working directory is the component's folder, which does not exist until the
	// repository is on disk: a run step before the checkout fails on a missing directory.
	firstStep := regexp.MustCompile(`(?m)^\s+steps:\n(?:\s*#.*\n|\s*\n)*\s+- (.*)$`)
	for _, name := range workflowNames {
		text := workflow(t, name)
		steps := firstStep.FindAllStringSubmatch(text, -1)
		if len(steps) == 0 {
			t.Errorf("%s has no steps", name)
		}
		for _, step := range steps {
			if !strings.HasPrefix(step[1], "uses: actions/checkout@") {
				t.Errorf("%s has a job whose first step is %q, not actions/checkout", name, step[1])
			}
		}
	}
}

func TestEveryVariableAndSecretCarriesTheComponentsPrefix(t *testing.T) {
	// Unprefixed, two components' sync scripts write the same repository variable, and each
	// overwrites the other's deploy role.
	for _, name := range workflowNames {
		text := workflow(t, name)
		for _, variable := range referenced(text) {
			if !strings.HasPrefix(variable, varPrefix) {
				t.Errorf("%s reads vars.%s, which is not prefixed %s", name, variable, varPrefix)
			}
		}
		for _, match := range secretsPattern.FindAllStringSubmatch(text, -1) {
			if !strings.HasPrefix(match[1], varPrefix) {
				t.Errorf("%s reads secrets.%s, which is not prefixed %s", name, match[1], varPrefix)
			}
		}
	}
	for output, variable := range syncMapping(t) {
		if !strings.HasPrefix(variable, varPrefix) {
			t.Errorf("sync-github.sh writes %s (from %s), which is not prefixed %s", variable, output, varPrefix)
		}
	}
	if tokenPattern.MatchString(syncScript(t)) {
		t.Error("sync-github.sh holds a component token")
	}
}

func TestTheSecretScanIsTheGitleaksCLIOverThisComponent(t *testing.T) {
	// gitleaks-action needs a paid licence on an organisation's repositories, and fails without
	// one. The CLI from its own image does not, and is scoped to this folder's history and its
	// config.
	ci := workflow(t, "ci.yml")
	for _, want := range []string{
		"ghcr.io/gitleaks/gitleaks:v",
		"git . --config " + component + "/.gitleaks.toml",
		"--log-opts=\"-- " + component + "\"",
		"--redact",
		"fetch-depth: 0",
		"safe.directory",
	} {
		if !strings.Contains(ci, want) {
			t.Errorf("ci.yml's secret scan lacks %q", want)
		}
	}
	if strings.Contains(ci, "gitleaks/gitleaks-action") {
		t.Error("ci.yml uses gitleaks-action, which needs a licence on an organisation's repositories")
	}
}

// --- The first deploy -------------------------------------------------------------------------

func TestTheFirstDeployRecipeRunsTheFirstDeployScript(t *testing.T) {
	if line := lineContaining(read(t, infraDir, "justfile"), "./scripts/first-deploy.sh"); line == "" {
		t.Error("infra/justfile has no recipe running scripts/first-deploy.sh")
	}
}

func TestTheFirstDeployBuildsWhatCDBuildsFromACommittedTree(t *testing.T) {
	script := firstDeployScript(t)
	cd := workflow(t, "cd.yml")
	for _, want := range []string{
		// The same image CD builds: the production target of the same Dockerfile, for the
		// platform the service runs, with the commit as GIT_SHA.
		"docker buildx build", "--platform linux/amd64", "--target production",
		"docker/Dockerfile.prod", "GIT_SHA=",
		// From a tree that is committed, so the tag names what was built.
		"status --porcelain",
		// Refuses until the stack has been applied and its secrets are pinned.
		"read_output image_repo", "read_output secrets_ready",
		// Records the image, then applies — with the refresh every apply here passes.
		"config set --plaintext serviceImage", "--refresh",
		// Ends on the deployed service, live and ready.
		"/health/live", "/health/ready",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("first-deploy.sh lacks %q", want)
		}
	}
	for _, want := range []string{"Dockerfile.prod", "target: production", "platforms: linux/amd64", "GIT_SHA="} {
		if !strings.Contains(cd, want) {
			t.Errorf("cd.yml lacks %q, which first-deploy.sh builds with", want)
		}
	}
	for _, line := range strings.Split(script, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "#") && strings.Contains(line, "--yes") {
			t.Errorf("first-deploy.sh passes --yes; the person running it types that: %s", strings.TrimSpace(line))
		}
	}
}

func TestTheFirstDeployMigratesAsCDDoes(t *testing.T) {
	script := firstDeployScript(t)
	cd := workflow(t, "cd.yml")
	for _, want := range firstDeployMigrationSteps {
		if !strings.Contains(script, want) {
			t.Errorf("first-deploy.sh's migration lacks %q", want)
		}
		if !strings.Contains(cd, want) {
			t.Errorf("cd.yml's migration lacks %q, which first-deploy.sh runs", want)
		}
	}
}

func TestTheSyncScriptRefusesToUngateCDBeforeTheFirstDeploy(t *testing.T) {
	// CD updates a service; it cannot create one. Un-gated before the first deploy, its first run
	// fails on a service that does not exist.
	script := syncScript(t)
	check := strings.Index(script, "read_output service_url")
	firstSet := strings.Index(script, "gh variable set")
	if check < 0 || firstSet < 0 || check > firstSet {
		t.Error("sync-github.sh must check service_url before it sets any variable")
	}
	if !strings.Contains(script, "just infra-first-deploy") {
		t.Error("sync-github.sh must print `just infra-first-deploy` as the fix for a missing service")
	}
}

func TestEveryConfigSetIsPlaintext(t *testing.T) {
	// A version id, a subject prefix and an image reference are not secrets, and the Pulumi CLI
	// refuses a value that looks like one — a UUID-shaped VersionId among them — unless told.
	scripts := []string{"sync-github.sh", "first-deploy.sh"}
	if _, err := os.Stat(filepath.Join(infraDir, "scripts", "secrets.sh")); err == nil {
		scripts = append(scripts, "secrets.sh")
	}
	for _, name := range scripts {
		for _, line := range strings.Split(read(t, infraDir, "scripts", name), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			if strings.Contains(line, "config set ") && !strings.Contains(line, "config set --plaintext ") {
				t.Errorf("%s: %s", name, strings.TrimSpace(line))
			}
		}
	}
}
