package stackconfig_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"{{ cookiecutter.go_module }}/infra/pulumi/internal/infratest"
	"{{ cookiecutter.go_module }}/infra/pulumi/internal/stackconfig"
)

const (
	slug       = "{{ cookiecutter.project_slug }}"
	repository = "{{ cookiecutter.github_repository }}"
)

func base() stackconfig.StackConfig {
	return stackconfig.Defaults(infratest.Project, "europe-west1", "dev", repository, slug)
}

func mustAccount(t *testing.T, c stackconfig.StackConfig, role string) string {
	t.Helper()
	id, err := c.AccountID(role)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func mustPool(t *testing.T, c stackconfig.StackConfig) string {
	t.Helper()
	id, err := c.PoolID("github")
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// --- The two environments ---------------------------------------------------------------------
//
// Environment names resources and AppEnvironment is what the application is told it is. They are
// read as one thing constantly, and the failure is quiet: a `dev` stack deploying a service that
// believes it is production serves no /docs and says nothing about why.

func TestTheStackEnvironmentNamesResources(t *testing.T) {
	c := base()
	if got := c.Name("api"); got != "api-dev" {
		t.Errorf("Name(api) = %q, want api-dev", got)
	}
	if got := c.Name(slug, "migrate"); !strings.HasSuffix(got, "-migrate-dev") {
		t.Errorf("Name(slug, migrate) = %q", got)
	}
}

func TestAFirstDeployStillAnswersDocs(t *testing.T) {
	if got := base().AppEnvironment; got != "staging" {
		t.Errorf("default appEnvironment = %q, want staging", got)
	}
}

func TestAnApplicationEnvironmentTheAppCannotParseIsRefused(t *testing.T) {
	// Caught here, or several minutes into an apply by a container that exits on startup.
	c := base()
	c.AppEnvironment = "dev"
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "appEnvironment") {
		t.Fatalf("Validate() = %v, want an appEnvironment error", err)
	}
}

func TestARepositoryThatIsNotOwnerSlashRepoIsRefused(t *testing.T) {
	// The attribute condition is built from it. A malformed value matches nothing — or something
	// nobody meant.
	for _, bad := range []string{"", "just-a-name", "/repo", "owner/", "a/b/c"} {
		c := base()
		c.GitHubRepository = bad
		if err := c.Validate(); err == nil {
			t.Errorf("githubRepository %q was accepted", bad)
		}
	}
}

// --- Naming -----------------------------------------------------------------------------------

var accountPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{4,28}[a-z0-9]$`)

func TestServiceAccountIDsFitWhatGCPAccepts(t *testing.T) {
	// A service-account id is 6-30 characters, and a long project name is the way past that. The
	// failure without this is a mid-apply rejection on a name nobody looked at, after the APIs and
	// the registry have already been created.
	for _, role := range []string{"run", "web", "ci"} {
		id := mustAccount(t, base(), role)
		if !accountPattern.MatchString(id) {
			t.Errorf("account id %q is not one GCP accepts", id)
		}
	}
}

func TestALongProjectNameIsShortenedRatherThanRejected(t *testing.T) {
	c := base()
	c.Slug, c.Environment = "a-very-long-service-name-indeed", "prod"
	run, ci := mustAccount(t, c, "run"), mustAccount(t, c, "ci")
	if len(run) > stackconfig.MaxAccountID || run == ci {
		t.Errorf("run=%q ci=%q", run, ci)
	}
}

func TestThePoolIDIsUniqueWithinTheProjectNotJustTheStack(t *testing.T) {
	// A pool id is project-global. Building it from the kind and the environment alone —
	// `github-dev` for everybody — means the second service generated from this template fails
	// `pulumi up` with a 409 on a pool the first one owns, and the id cannot be freed for 30 days.
	other := base()
	other.Slug = "another-service"
	ours := mustPool(t, base())
	if ours == mustPool(t, other) {
		t.Errorf("two services share pool id %q", ours)
	}
	if prefix := slug[:min(8, len(slug))]; !strings.HasPrefix(ours, strings.TrimRight(prefix, "-")) {
		t.Errorf("pool id %q does not start with the slug", ours)
	}
}

func TestTwoLongSlugsSharingAPrefixDoNotTrimIntoOneName(t *testing.T) {
	// `platform-billing-service-api` and `platform-billing-service-web` both cut down to
	// `platform-billing-serv`. The digest is taken over the untrimmed slug, so what the trim
	// discards still reaches the result.
	api, web := base(), base()
	api.Slug, web.Slug = "platform-billing-service-api", "platform-billing-service-web"
	if mustPool(t, api) == mustPool(t, web) {
		t.Error("two distinct long slugs trimmed into one pool id")
	}
}

func TestTheSameSlugInTwoRepositoriesIsTwoPools(t *testing.T) {
	ours, theirs := base(), base()
	ours.Slug, theirs.Slug = "orders", "orders"
	ours.GitHubRepository, theirs.GitHubRepository = "acme/orders", "other-team/orders"
	if mustPool(t, ours) == mustPool(t, theirs) {
		t.Error("the same slug in two repositories shares a pool id")
	}
}

func TestThePoolIDIsTheSameEveryTimeItIsComputed(t *testing.T) {
	// A random suffix would be more unique and unusable: Pulumi would replace the pool on every
	// run and drop each old id into GCP's 30-day hold.
	first, second := base(), base()
	if a, b := mustPool(t, first), mustPool(t, second); a != b {
		t.Errorf("pool id is not deterministic: %q then %q", a, b)
	}
}

var poolPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{2,30}[a-z0-9]$`)

func TestALongProjectNameStillYieldsAPoolIDGCPAccepts(t *testing.T) {
	c := base()
	c.Slug, c.Environment = "a-very-long-service-name-indeed", "prod"
	if pool := mustPool(t, c); !poolPattern.MatchString(pool) {
		t.Errorf("pool id %q is not one GCP accepts", pool)
	}
}

// --- Loading ----------------------------------------------------------------------------------

func load(t *testing.T, config map[string]string) (stackconfig.StackConfig, error) {
	t.Helper()
	var got stackconfig.StackConfig
	var loadErr error
	_, err := infratest.TryRun(slug, config, func(ctx *pulumi.Context) error {
		got, loadErr = stackconfig.Load(ctx)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return got, loadErr
}

func required() map[string]string {
	return map[string]string{
		"project":          infratest.Project,
		"region":           "europe-west1",
		"environment":      "dev",
		"githubRepository": repository,
		"slug":             slug,
	}
}

func TestLoadAppliesEveryDefault(t *testing.T) {
	got, err := load(t, required())
	if err != nil {
		t.Fatal(err)
	}
	if want := base(); got != want {
		t.Errorf("Load() = %+v\nwant      %+v", got, want)
	}
	if got.FrontendEnabled {
		t.Error("frontendEnabled must default to false: a service with no browser client needs no CORS")
	}
}

func TestLoadReadsTheFrontendSwitch(t *testing.T) {
	config := required()
	config["frontendEnabled"] = "true"
	got, err := load(t, config)
	if err != nil {
		t.Fatal(err)
	}
	if !got.FrontendEnabled {
		t.Error("frontendEnabled: true was not read")
	}
}

func TestLoadRefusesATypoRatherThanDefaultingIt(t *testing.T) {
	// A value that silently becomes a default is a stack doing something nobody configured.
	for key, value := range map[string]string{"maxInstances": "four", "frontendEnabled": "yes please"} {
		config := required()
		config[key] = value
		if _, err := load(t, config); err == nil || !strings.Contains(err.Error(), key) {
			t.Errorf("%s=%q: Load() = %v, want an error naming the key", key, value, err)
		}
	}
}

func TestLoadNamesEveryMissingRequiredKey(t *testing.T) {
	_, err := load(t, map[string]string{})
	if err == nil {
		t.Fatal("Load() accepted an empty configuration")
	}
	for key := range required() {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("error does not name %s: %v", key, err)
		}
	}
}
