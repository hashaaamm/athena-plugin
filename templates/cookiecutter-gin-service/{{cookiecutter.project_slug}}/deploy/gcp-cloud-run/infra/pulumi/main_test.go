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
	slug       = "{{ cookiecutter.project_slug }}"
	repository = "{{ cookiecutter.github_repository }}"

	serviceType = "gcp:cloudrunv2/service:Service"
	accountType = "gcp:serviceaccount/account:Account"
)

// allExports is every name the stack exports, in every configuration.
var allExports = []string{
	"cloud_run_migrate_job",
	"cloud_run_service",
	"cloud_sql_instance",
	"deploy_sa",
	"gcp_project",
	"gcp_region",
	"image_repo",
	"runtime_sa",
	"service_url",
	"wif_provider",
}

func baseConfig() stackconfig.StackConfig {
	return stackconfig.Defaults(infratest.Project, "europe-west1", "dev", repository, slug)
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
const browserOrigin = "https://{{ cookiecutter.project_slug }}-web-abc123-ew.a.run.app"

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
	return infratest.Get(s.Find(t, serviceType, app).Inputs, "template", "containers", 0)
}

func (s stack) backendEnv(t *testing.T) map[string]string {
	t.Helper()
	return infratest.EnvOf(s.container(t, slug))
}

// --- Exports ----------------------------------------------------------------------------------

func TestEveryExportExistsInEveryConfiguration(t *testing.T) {
	// A name that is exported in one configuration and absent in another is indistinguishable,
	// to sync-github.sh, from a stack that was never applied.
	for _, mutate := range []func(*stackconfig.StackConfig){nil, withBrowserClient} {
		got := slices.Sorted(maps.Keys(deployed(t, mutate).exports))
		if !slices.Equal(got, allExports) {
			t.Errorf("exports = %v\nwant      %v", got, allExports)
		}
	}
}

func TestThePipelineExportsAreNeverEmpty(t *testing.T) {
	s := deployed(t, nil)
	for _, name := range []string{
		"gcp_project", "gcp_region", "image_repo", "wif_provider", "deploy_sa", "runtime_sa",
		"cloud_run_service", "service_url",
	} {
		if s.exports[name] == "" {
			t.Errorf("%s is empty", name)
		}
	}
	if want := "europe-west1-docker.pkg.dev/" + infratest.Project + "/" + slug + "-dev"; s.exports["image_repo"] != want {
		t.Errorf("image_repo = %q, want %q", s.exports["image_repo"], want)
	}
	if !strings.HasSuffix(s.exports["wif_provider"], "/providers/github") {
		t.Errorf("wif_provider = %q is not a provider resource name", s.exports["wif_provider"])
	}
}

// --- Browser clients --------------------------------------------------------------------------

func TestTheStackServesOneServiceUnderTwoAccounts(t *testing.T) {
	// Backend only: a browser client is its own repository with its own stack (ADR-0022), so it
	// adds nothing here whatever this stack is configured with.
	for _, mutate := range []func(*stackconfig.StackConfig){nil, withBrowserClient} {
		s := deployed(t, mutate)
		if services := s.OfType(serviceType); len(services) != 1 {
			t.Errorf("%d Cloud Run services, want 1", len(services))
		}
		if accounts := s.OfType(accountType); len(accounts) != 2 {
			t.Errorf("%d service accounts, want the runtime and the CI one", len(accounts))
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
	// A literal here is a stack whose name says `dev` and whose service believes it is prod.
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
}

// --- Wiring -----------------------------------------------------------------------------------

func TestEveryGCPResourceUsesTheExplicitProvider(t *testing.T) {
	// Pulumi.yaml disables the default GCP provider. A resource that did not inherit the explicit
	// one would fail at apply time with "default provider disabled" — here, it fails the build.
	for _, mutate := range []func(*stackconfig.StackConfig){nil, withBrowserClient} {
		for _, res := range deployed(t, mutate).Resources() {
			if res.Custom && strings.HasPrefix(res.Type, "gcp:") && !strings.Contains(res.Provider, "pulumi:providers:gcp::gcp") {
				t.Errorf("%s %q uses provider %q", res.Type, res.Name, res.Provider)
			}
		}
	}
}

func TestEverythingWaitsForTheAPIs(t *testing.T) {
	// Without the edge the first `up` on a fresh project fails halfway through with "API not
	// enabled", with half the stack already created.
	s := deployed(t, withBrowserClient)
	for _, res := range s.Resources() {
		if !res.Custom || !strings.HasPrefix(res.Type, "gcp:") || res.Type == "gcp:projects/service:Service" {
			continue
		}
		if !slices.ContainsFunc(res.Dependencies, func(urn string) bool {
			return strings.Contains(urn, "gcp:projects/service:Service::api-run")
		}) {
			t.Errorf("%s %q does not wait for the APIs", res.Type, res.Name)
		}
	}
}
