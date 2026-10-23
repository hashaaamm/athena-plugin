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
	"cloud_run_frontend_service",
	"cloud_run_migrate_job",
	"cloud_run_service",
	"cloud_sql_instance",
	"deploy_sa",
	"frontend_url",
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

func withFrontend(c *stackconfig.StackConfig) { c.FrontendEnabled = true }

func (s stack) container(t *testing.T, app string) any {
	t.Helper()
	return infratest.Get(s.Find(t, serviceType, app).Inputs, "template", "containers", 0)
}

func (s stack) backendEnv(t *testing.T) map[string]string {
	t.Helper()
	return infratest.EnvOf(s.container(t, slug))
}

// --- Exports ----------------------------------------------------------------------------------

func TestEveryExportExistsWhetherOrNotTheFrontendIsEnabled(t *testing.T) {
	// A name that is exported in one configuration and absent in another is indistinguishable,
	// to sync-github.sh, from a stack that was never applied.
	for _, mutate := range []func(*stackconfig.StackConfig){nil, withFrontend} {
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

// --- The frontend switch ----------------------------------------------------------------------

func TestWithoutAFrontendThereIsOneServiceAndNoCORS(t *testing.T) {
	s := deployed(t, nil)
	if services := s.OfType(serviceType); len(services) != 1 {
		t.Errorf("%d Cloud Run services, want 1", len(services))
	}
	if s.Has(accountType, "web-runtime-sa") {
		t.Error("an account for a frontend that does not exist")
	}
	if s.exports["cloud_run_frontend_service"] != "" || s.exports["frontend_url"] != "" {
		t.Errorf("frontend exports are set without a frontend: %q %q",
			s.exports["cloud_run_frontend_service"], s.exports["frontend_url"])
	}
	// No browser client, so no browser origin and nothing for CORS to allow.
	env := s.backendEnv(t)
	for _, name := range []string{"CORS_ORIGINS", "REFRESH_TRANSPORT"} {
		if _, ok := env[name]; ok {
			t.Errorf("%s is set on a backend with no frontend", name)
		}
	}
}

func TestWithAFrontendTheBackendAllowsItsDeployedOrigin(t *testing.T) {
	// Otherwise the SPA loads, makes exactly one request, and the browser refuses the answer —
	// which reads as an API outage rather than as a missing allowlist entry. The origin is a Cloud
	// Run URL, so it cannot be typed in ahead of time; it has to be wired from the stack.
	s := deployed(t, withFrontend)
	web := slug + "-web"
	if s.exports["cloud_run_frontend_service"] != web+"-dev" {
		t.Errorf("cloud_run_frontend_service = %q", s.exports["cloud_run_frontend_service"])
	}
	if got := s.backendEnv(t)["CORS_ORIGINS"]; got == "" || got != s.exports["frontend_url"] {
		t.Errorf("CORS_ORIGINS = %q, frontend_url = %q", got, s.exports["frontend_url"])
	}
}

func TestTheFrontendServiceServesFilesAndNothingElse(t *testing.T) {
	s := deployed(t, withFrontend)
	web := slug + "-web"
	svc := s.Find(t, serviceType, web)
	webAccount, _ := s.Find(t, accountType, "web-runtime-sa").Inputs["accountId"].(string)
	if got, _ := infratest.Get(svc.Inputs, "template", "serviceAccount").(string); !strings.HasPrefix(got, webAccount+"@") {
		t.Errorf("the frontend runs as %q, not its own account %q", got, webAccount)
	}
	c := s.container(t, web)
	if got := infratest.Get(c, "startupProbe", "httpGet", "path"); got != "/" {
		t.Errorf("frontend probe path = %v", got)
	}
	if envs, _ := infratest.Get(c, "envs").([]any); len(envs) != 0 {
		t.Errorf("frontend envs = %v; Vite inlined everything at build time", envs)
	}
	if volumes, _ := infratest.Get(svc.Inputs, "template", "volumes").([]any); len(volumes) != 0 {
		t.Errorf("frontend volumes = %v", volumes)
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
	for _, mutate := range []func(*stackconfig.StackConfig){nil, withFrontend} {
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
	s := deployed(t, withFrontend)
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
