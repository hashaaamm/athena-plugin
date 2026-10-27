package stackconfig_test

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"{{ cookiecutter.go_module }}/infra/pulumi/internal/infratest"
	"{{ cookiecutter.go_module }}/infra/pulumi/internal/stackconfig"
)

const (
	slug       = "{{ cookiecutter.__name }}"
	repository = "{{ cookiecutter.github_repository }}"
)

func base() stackconfig.StackConfig {
	return stackconfig.Defaults(infratest.AccountID, infratest.Region, "dev", repository, slug)
}

func refused(t *testing.T, c stackconfig.StackConfig, key string) {
	t.Helper()
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), key) {
		t.Errorf("Validate() = %v, want an error naming %s", err, key)
	}
}

// --- The first deploy -------------------------------------------------------------------------

func TestThereIsNoServiceImageUntilTheFirstDeployRecordsOne(t *testing.T) {
	// No placeholder: empty is the stack `just infra-up` applies, and it creates no service.
	if got := base().ServiceImage; got != "" {
		t.Errorf("default serviceImage = %q, want empty", got)
	}
	if err := base().Validate(); err != nil {
		t.Errorf("the default configuration is refused: %v", err)
	}
}

func TestAServiceImageNamesExactlyOneImage(t *testing.T) {
	// CD never pushes :latest, and a rollback has to be able to name what it rolls back to.
	for _, good := range []string{
		"registry.example.com/team/api-dev:0123abc",
		"localhost:5000/api:1",
		"registry.example.com/api@sha256:0123456789abcdef",
	} {
		c := base()
		c.ServiceImage = good
		if err := c.Validate(); err != nil {
			t.Errorf("serviceImage %q refused: %v", good, err)
		}
	}
	for _, bad := range []string{
		"registry.example.com/api", "registry.example.com/api:latest", "localhost:5000/api",
		"registry.example.com/api:", "api :1", "registry.example.com/api@md5:00",
	} {
		c := base()
		c.ServiceImage = bad
		refused(t, c, "serviceImage")
	}
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
	c := base()
	c.AppEnvironment = "dev"
	refused(t, c, "appEnvironment")
}

func TestARepositoryThatIsNotOwnerSlashRepoIsRefused(t *testing.T) {
	// The trust policies' subject is built from it. A malformed value matches nothing — or
	// something nobody meant.
	for _, bad := range []string{"", "just-a-name", "/repo", "owner/", "a/b/c"} {
		c := base()
		c.GitHubRepository = bad
		if err := c.Validate(); err == nil {
			t.Errorf("githubRepository %q was accepted", bad)
		}
	}
}

func TestTheSubjectPrefixDefaultsToTheNameOnlyForm(t *testing.T) {
	if got := base().SubjectPrefix(); got != "repo:"+repository {
		t.Errorf("SubjectPrefix() = %q", got)
	}
	c := base()
	c.GitHubSubjectPrefix = "repo:acme@123/billing-api@456"
	if got := c.SubjectPrefix(); got != "repo:acme@123/billing-api@456" {
		t.Errorf("SubjectPrefix() = %q, want the configured prefix", got)
	}
}

func TestASubjectPrefixThatIsNotExactIsRefused(t *testing.T) {
	// StringEquals matches it, so a wildcard would not work — and a prefix that ends in ':' or
	// does not start with repo: matches nothing GitHub issues.
	for _, bad := range []string{"acme/billing-api", "repo:acme@*/billing-api@*", "repo:acme/x:", "repo:a b"} {
		c := base()
		c.GitHubSubjectPrefix = bad
		refused(t, c, "githubSubjectPrefix")
	}
}

// --- The account guard and the shapes AWS accepts ---------------------------------------------

func TestAnAccountIDThatIsNotTwelveDigitsIsRefused(t *testing.T) {
	// It becomes allowedAccountIds. A typo here is a guard that refuses the right account — or,
	// with a different typo, admits the wrong one.
	for _, bad := range []string{"12345678901", "1234567890123", "abcdefghijkl", "1234-5678-9012"} {
		c := base()
		c.AccountID = bad
		refused(t, c, "accountId")
	}
}

func TestARegionThatIsNotARegionCodeIsRefused(t *testing.T) {
	for _, bad := range []string{"Ireland", "eu-west", "euwest1", "EU-WEST-1"} {
		c := base()
		c.Region = bad
		refused(t, c, "region")
	}
	for _, good := range []string{"eu-west-1", "us-east-1", "ap-southeast-2", "us-gov-west-1"} {
		c := base()
		c.Region = good
		if err := c.Validate(); err != nil {
			t.Errorf("region %q: %v", good, err)
		}
	}
}

func TestGovCloudGetsItsOwnPartition(t *testing.T) {
	c := base()
	if c.Partition() != "aws" {
		t.Errorf("partition = %q", c.Partition())
	}
	c.Region = "us-gov-west-1"
	if c.Partition() != "aws-us-gov" {
		t.Errorf("GovCloud partition = %q", c.Partition())
	}
}

func TestExpressModeNeverScalesToZero(t *testing.T) {
	for _, r := range [][2]int{
		{0, 4}, // Express Mode runs at least one task
		{3, 2}, // an empty range
	} {
		c := base()
		c.MinTasks, c.MaxTasks = r[0], r[1]
		refused(t, c, "minTasks")
	}
}

func TestOnlyFargateSizesAreAccepted(t *testing.T) {
	for _, bad := range [][2]string{
		{"256", "4096"}, // too much memory for a quarter vCPU
		{"1024", "512"}, // too little for a whole one
		{"300", "512"},  // not a CPU size at all
	} {
		c := base()
		c.CPU, c.Memory = bad[0], bad[1]
		refused(t, c, "cpu")
	}
}

func TestTheVPCSpansTwoOrThreeZones(t *testing.T) {
	// Fewer than two is a network configuration Express Mode refuses, and a subnet group RDS does.
	for _, bad := range []int{1, 4} {
		c := base()
		c.AvailabilityZoneCount = bad
		refused(t, c, "availabilityZoneCount")
	}
}

func TestASlugECRAndRDSWouldRefuseIsRefusedHere(t *testing.T) {
	for _, bad := range []string{"Billing", "1-billing", "billing_api", "billing-"} {
		c := base()
		c.Slug = bad
		refused(t, c, "slug")
	}
}

func TestTheStateBucketIsTheOneTheBootstrapScriptCreates(t *testing.T) {
	if got := base().StateBucket(); got != "pulumi-state-"+infratest.AccountID+"-"+infratest.Region {
		t.Errorf("StateBucket() = %q", got)
	}
}
{%- if cookiecutter.use_postgres == "yes" %}

func TestDatabaseStorageStaysBetweenTheFloorAndTheCeiling(t *testing.T) {
	for _, bad := range []int{19, stackconfig.MaxDatabaseStorageGB} {
		c := base()
		c.DatabaseStorageGB = bad
		refused(t, c, "databaseStorageGb")
	}
}
{%- endif %}

// --- Browser clients --------------------------------------------------------------------------
//
// corsOrigins and refreshTransport become CORS_ORIGINS and REFRESH_TRANSPORT on the service, and the
// service validates both at start-up. Each refusal below is one the service would make anyway —
// several minutes into an apply, as a deployment that never becomes healthy.

func TestOriginsAreParsedLikeTheServiceParsesThem(t *testing.T) {
	got := stackconfig.SplitOrigins(" https://app.example.com , ,http://localhost:3000,")
	if want := []string{"https://app.example.com", "http://localhost:3000"}; !slices.Equal(got, want) {
		t.Errorf("SplitOrigins = %q, want %q", got, want)
	}
	if got := stackconfig.SplitOrigins(""); len(got) != 0 {
		t.Errorf("SplitOrigins(\"\") = %q, want none", got)
	}
}

func TestAnythingButAnOriginIsRefused(t *testing.T) {
	// A trailing slash or a path never matches a real Origin header, so the entry would be dead
	// configuration that looks alive; `*` cannot carry credentials.
	for _, bad := range []string{
		"*", "https://*.example.com", "app.example.com", "ftp://app.example.com",
		"https://app.example.com/", "https://app.example.com/path", "https://user@app.example.com",
		"https://app.example.com?x=1", "https://",
	} {
		c := base()
		c.CORSOrigins = []string{bad}
		refused(t, c, "corsOrigins")
	}
	c := base()
	c.CORSOrigins = []string{"https://d1234abcd.cloudfront.net", "http://localhost:3000"}
	if err := c.Validate(); err != nil {
		t.Errorf("two valid origins: %v", err)
	}
}
{%- if cookiecutter.use_postgres == "yes" %}

func TestTheRefreshTransportIsBodyOrCookie(t *testing.T) {
	c := base()
	c.RefreshTransport = "header"
	refused(t, c, "refreshTransport")
}

func TestACookieTransportNeedsAnOrigin(t *testing.T) {
	// The CSRF check in front of the cookie routes accepts listed origins only, so with none listed
	// every refresh is refused and a browser session never survives a reload.
	c := base()
	c.RefreshTransport = "cookie"
	refused(t, c, "requires corsOrigins")
	c.CORSOrigins = []string{"https://d1234abcd.cloudfront.net"}
	if err := c.Validate(); err != nil {
		t.Errorf("cookie with an origin: %v", err)
	}
}
{%- endif %}

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
		"accountId":        infratest.AccountID,
		"region":           infratest.Region,
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
	if want := base(); !reflect.DeepEqual(got, want) {
		t.Errorf("Load() = %+v\nwant      %+v", got, want)
	}
	// No browser may call a service until somebody says which one.
	if len(got.CORSOrigins) != 0 {
		t.Errorf("corsOrigins defaults to %q, want none", got.CORSOrigins)
	}
{%- if cookiecutter.use_postgres == "yes" %}
	if got.RefreshTransport != "body" {
		t.Errorf("refreshTransport defaults to %q, want body", got.RefreshTransport)
	}
{%- endif %}
	if got.MinTasks != 1 || got.CPU != "256" || got.Memory != "512" || got.AvailabilityZoneCount != 2 {
		t.Errorf("defaults = %+v", got)
	}
}

func TestLoadReadsEveryOptionalKey(t *testing.T) {
	config := required()
	config["corsOrigins"] = "https://d1234abcd.cloudfront.net, http://localhost:3000"
{%- if cookiecutter.use_postgres == "yes" %}
	config["refreshTransport"] = "cookie"
{%- endif %}
	config["minTasks"] = "2"
	config["maxTasks"] = "6"
	config["cpu"] = "512"
	config["memory"] = "1024"
	config["availabilityZoneCount"] = "3"
	got, err := load(t, config)
	if err != nil {
		t.Fatal(err)
	}
	if got.MinTasks != 2 || got.MaxTasks != 6 || got.CPU != "512" ||
		got.Memory != "1024" || got.AvailabilityZoneCount != 3 {
		t.Errorf("Load() = %+v", got)
	}
	if want := []string{"https://d1234abcd.cloudfront.net", "http://localhost:3000"}; !slices.Equal(got.CORSOrigins, want) {
		t.Errorf("corsOrigins = %q, want %q", got.CORSOrigins, want)
	}
{%- if cookiecutter.use_postgres == "yes" %}
	if got.RefreshTransport != "cookie" {
		t.Errorf("refreshTransport = %q, want cookie", got.RefreshTransport)
	}
{%- endif %}
}
{%- if cookiecutter.use_postgres == "yes" %}

// secrets_ready is what sync-github.sh checks before it un-gates CD, and the signing key is the
// one secret the service cannot start without.
func TestTheSecretsAreReadyOnlyOnceTheSigningKeyHasAVersion(t *testing.T) {
	got, err := load(t, required())
	if err != nil {
		t.Fatal(err)
	}
	if got.JWTSecretVersion != "" || got.SecretsReady() {
		t.Errorf("jwtSecretVersion = %q, ready = %v before anything is configured", got.JWTSecretVersion, got.SecretsReady())
	}
	config := required()
	config["jwtSecretVersion"] = " a1b2c3d4-0000-0000-0000-000000000000 "
	config["databaseAppRole"] = "orders_api"
	if got, err = load(t, config); err != nil {
		t.Fatal(err)
	}
	if got.JWTSecretVersion != "a1b2c3d4-0000-0000-0000-000000000000" || !got.SecretsReady() || got.DatabaseAppRole != "orders_api" {
		t.Errorf("Load() = %+v", got)
	}
}
{%- else %}

// Without a database there is no signing key to wait for.
func TestWithoutADatabaseTheSecretsAreAlwaysReady(t *testing.T) {
	if !base().SecretsReady() {
		t.Error("secrets_ready is false on a stack with nothing to generate")
	}
}
{%- endif %}

func TestLoadRefusesATypoRatherThanDefaultingIt(t *testing.T) {
	// A value that silently becomes a default is a stack doing something nobody configured.
	typos := map[string]string{
		"maxTasks":              "four",
		"availabilityZoneCount": "two",
		"corsOrigins":           "app.example.com",
	}
{%- if cookiecutter.use_postgres == "yes" %}
	typos["refreshTransport"] = "cookies"
{%- endif %}
	for key, value := range typos {
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
