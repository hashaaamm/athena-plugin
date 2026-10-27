// Package stackconfig is the typed stack configuration.
//
// Every value the stack reads is resolved here, once. Components take a StackConfig rather than
// calling config.New themselves, which is what keeps a component constructible in a test and stops
// configuration keys being invented at three different call sites.
//
// StackConfig is passed by value everywhere, and that is how it is frozen: a component holds its
// own copy, so nothing it does can change what the next component sees.
package stackconfig

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi/config"
)

const (
	// MaxRoleName is IAM's upper bound on a role name. The environment suffix and the role suffix
	// are what make two roles different, so the slug is what has to give when a name is long.
	MaxRoleName = 64

	// MaxDBIdentifier is RDS's upper bound on an instance identifier, which must also start with a
	// letter, hold only letters, digits and single hyphens, and not end in a hyphen.
	MaxDBIdentifier = 63

	// MinDatabaseStorageGB is the smallest gp3 volume RDS for PostgreSQL accepts.
	MinDatabaseStorageGB = 20

	// MaxDatabaseStorageGB is the storage-autoscaling ceiling the instance is created with. RDS
	// grows the volume up to it on its own, and never shrinks it back — which is why the starting
	// size is the floor and the ceiling is a deliberate number rather than "unlimited".
	MaxDatabaseStorageGB = 100

	// DefaultAppEnvironment is the honest reading of a first deploy: production-shaped logging and
	// a placeholder database password still refused, with /docs reachable, because the first thing
	// anybody does with a new URL is open it and look for the API.
	DefaultAppEnvironment = "staging"
)

// AppEnvironments is what ENVIRONMENT may be set to on the deployed service. It is the set
// backend/internal/config accepts and nothing else parses: a typo here is a container that exits
// on its first configuration load, several minutes into an apply.
var AppEnvironments = []string{"local", "test", "staging", "production"}
{%- if cookiecutter.use_postgres == "yes" %}

// RefreshTransports is what REFRESH_TRANSPORT may be set to on the deployed service: the set
// backend/internal/config accepts, for the same reason as AppEnvironments.
var RefreshTransports = []string{"body", "cookie"}
{%- endif %}

// FargateSizes is every cpu/memory pair the stack accepts, in the units ECS takes them: CPU units
// (1024 to a vCPU) and MiB. Fargate allows only fixed combinations, and Express Mode caps memory at
// 8192 MiB; anything else is a task ECS refuses to place, discovered after the load balancer and
// the security groups already exist.
var FargateSizes = map[string][]string{
	"256":  {"512", "1024", "2048"},
	"512":  {"1024", "2048", "3072", "4096"},
	"1024": {"2048", "3072", "4096", "5120", "6144", "7168", "8192"},
	"2048": {"4096", "5120", "6144", "7168", "8192"},
	"4096": {"8192"},
}

var (
	accountPattern = regexp.MustCompile(`^[0-9]{12}$`)
	regionPattern  = regexp.MustCompile(`^[a-z]{2}(-gov)?-[a-z]+-[0-9]+$`)
	// Lowercase, starting with a letter. ECR refuses uppercase, and RDS refuses an identifier that
	// starts with a digit; the slug is the start of both.
	slugPattern = regexp.MustCompile(`^[a-z][a-z0-9-]*[a-z0-9]$`)
)

// StackConfig is every value the stack reads, resolved and validated.
type StackConfig struct {
	// AccountID is the AWS account the stack deploys into. The provider refuses to plan anything
	// against credentials for any other account — the wrong-account guard, before a single
	// resource is read.
	AccountID string
	Region    string

	// Environment is the STACK's environment. It names resources — `api-dev`, and every role,
	// secret and repository with the same suffix — and decides IsProduction. The application has
	// never heard of it.
	Environment string

	// GitHubRepository is `owner/repo`. The CI roles' trust policies are built from it, and it is
	// the single value that stops any repository on GitHub from assuming them.
	GitHubRepository string

	// GitHubSubjectPrefix is the start of the `sub` claim GitHub puts in this repository's OIDC
	// tokens, everything before `:environment:…` or `:pull_request`. Empty means the name-only form,
	// `repo:<owner>/<repo>`.
	//
	// GitHub now issues an immutable form for many repositories —
	// `repo:<owner>@<owner-id>/<repo>@<repo-id>` — so that a renamed or re-registered account cannot
	// inherit the trust. A trust policy written for the name-only form then matches no token at all,
	// and CI fails with "Not authorized to perform sts:AssumeRoleWithWebIdentity". The repository
	// reports its own prefix: `gh api repos/<owner>/<repo>/actions/oidc/customization/sub --jq
	// .sub_claim_prefix`. infra/README.md has the step, and sync-github.sh refuses to un-gate CD
	// while the two disagree.
	GitHubSubjectPrefix string

	// Slug is the base name every resource is derived from — the cookiecutter slug.
	Slug string

	// AppEnvironment is what the APPLICATION is told it is running as, which is a different
	// question from what this stack is called. It becomes ENVIRONMENT on the ECS task and decides
	// how the service behaves: whether /docs and /openapi.json are served, whether a placeholder
	// database password is refused.
	//
	// They are set separately because they vary separately. A `dev` stack that anyone outside the
	// team can reach is `production` to the application; a `prod` stack standing in for a
	// customer demo is not. Set it to `production` in the stack real users reach. See
	// infra/README.md.
	AppEnvironment string

	// MinTasks and MaxTasks bound the Express service's auto-scaling. Express Mode cannot scale to
	// zero: the minimum is one running task, billed whether or not anyone calls it.
	MinTasks int
	MaxTasks int

	// CPU and Memory are the backend task's size in ECS units, a pair from FargateSizes.
	CPU    string
	Memory string

	// AvailabilityZoneCount is how many AZs the VPC spans: one public and one isolated subnet in
	// each. Two is the minimum Express Mode accepts for a network configuration, and the minimum
	// an RDS subnet group accepts.
	AvailabilityZoneCount int

	// CORSOrigins are the browser origins allowed to call the API, from the comma-separated
	// `corsOrigins`. They become CORS_ORIGINS on the service when there are any; empty — the
	// default — means no browser on another origin may call it. A browser client is its own
	// repository with its own stack (ADR-0022), so its address is typed in here rather than read
	// from a resource this stack owns: the react-app stack's `site_url` output.
	CORSOrigins []string
{%- if cookiecutter.use_postgres == "yes" %}

	// RefreshTransport is REFRESH_TRANSPORT on the service: `body` for a server-to-server caller,
	// `cookie` for a browser, which must not hold the refresh token where script can read it.
	RefreshTransport string
{%- endif %}
{%- if cookiecutter.use_postgres == "yes" %}

	// DatabaseInstanceClass, DatabaseStorageGB and DatabaseMultiAZ are the RDS knobs. The defaults
	// are the smallest Graviton burstable class, the gp3 floor, and one AZ; infra/README.md has
	// what each one costs you.
	DatabaseInstanceClass string
	DatabaseStorageGB     int
	DatabaseMultiAZ       bool
{%- endif %}
{%- if cookiecutter.use_sentry == "yes" %}

	// SentryDSNVersion is the Secrets Manager VersionId of the Sentry DSN the service pins. Empty
	// means "not mounted".
	//
	// Empty by default, and the default has to stay applyable: a secret with no value is a task
	// that cannot start rather than a service that starts without error tracking. The stack
	// creates the empty secret; a human runs `aws secretsmanager put-secret-value` and puts the
	// VersionId it prints here; only then does the mount appear. Rotating the DSN is the same two
	// steps with the new id, which deploys a new task definition.
	SentryDSNVersion string
{%- endif %}
}

// Defaults returns a configuration holding the required values and every default. Load starts
// from it, and so do the tests, so a default is written down exactly once.
func Defaults(accountID, region, environment, githubRepository, slug string) StackConfig {
	c := StackConfig{
		AccountID:        accountID,
		Region:           region,
		Environment:      environment,
		GitHubRepository: githubRepository,
		Slug:             slug,
		AppEnvironment:   DefaultAppEnvironment,
		// One task is the floor Express Mode allows; see the cost table in infra/README.md.
		MinTasks:              1,
		MaxTasks:              4,
		CPU:                   "256",
		Memory:                "512",
		AvailabilityZoneCount: 2,
	}
{%- if cookiecutter.use_postgres == "yes" %}
	c.RefreshTransport = "body"
	c.DatabaseInstanceClass = "db.t4g.micro"
	c.DatabaseStorageGB = MinDatabaseStorageGB
{%- endif %}
	return c
}

// Validate refuses a configuration the stack or the application cannot work with, at load time,
// rather than letting it surface as a resource AWS rejects half way through an apply.
func (c StackConfig) Validate() error {
	var problems []error
	required := [][2]string{
		{"accountId", c.AccountID},
		{"region", c.Region},
		{"environment", c.Environment},
		{"githubRepository", c.GitHubRepository},
		{"slug", c.Slug},
	}
	for _, field := range required {
		if strings.TrimSpace(field[1]) == "" {
			problems = append(problems, fmt.Errorf("%s is required", field[0]))
		}
	}
	if c.AccountID != "" && !accountPattern.MatchString(c.AccountID) {
		problems = append(problems, fmt.Errorf("accountId %q is not 12 digits", c.AccountID))
	}
	if c.Region != "" && !regionPattern.MatchString(c.Region) {
		problems = append(problems, fmt.Errorf("region %q is not a region code such as eu-west-1", c.Region))
	}
	if c.Slug != "" && !slugPattern.MatchString(c.Slug) {
		problems = append(problems, fmt.Errorf(
			"slug %q must be lowercase letters, digits and hyphens, starting with a letter", c.Slug))
	}
	if !slices.Contains(AppEnvironments, c.AppEnvironment) {
		problems = append(problems, fmt.Errorf(
			"appEnvironment %q is not one of %v", c.AppEnvironment, AppEnvironments))
	}
	// One slash, two non-empty halves. Anything else builds a trust-policy subject that matches
	// nothing — or, worse, something nobody meant.
	if owner, repo, ok := strings.Cut(c.GitHubRepository, "/"); !ok || owner == "" || repo == "" ||
		strings.Contains(repo, "/") {
		problems = append(problems, fmt.Errorf(
			"githubRepository %q is not owner/repo", c.GitHubRepository))
	}
	// Exact, as the trust policy matches it with StringEquals: a wildcard here would be a trust
	// policy that admits more than one repository.
	if p := c.GitHubSubjectPrefix; p != "" &&
		(!strings.HasPrefix(p, "repo:") || strings.ContainsAny(p, "*? ") || strings.HasSuffix(p, ":")) {
		problems = append(problems, fmt.Errorf(
			"githubSubjectPrefix %q is not a subject prefix such as repo:<owner>@<id>/<repo>@<id>", p))
	}
	if c.MinTasks < 1 || c.MinTasks > c.MaxTasks {
		problems = append(problems, fmt.Errorf(
			"minTasks %d / maxTasks %d is not a valid range: Express Mode runs at least one task",
			c.MinTasks, c.MaxTasks))
	}
	if !slices.Contains(FargateSizes[c.CPU], c.Memory) {
		problems = append(problems, fmt.Errorf(
			"cpu %q / memory %q is not a Fargate size Express Mode accepts; see stackconfig.FargateSizes",
			c.CPU, c.Memory))
	}
	if c.AvailabilityZoneCount < 2 || c.AvailabilityZoneCount > 3 {
		problems = append(problems, fmt.Errorf(
			"availabilityZoneCount %d is not 2 or 3", c.AvailabilityZoneCount))
	}
	for _, origin := range c.CORSOrigins {
		if err := validateOrigin(origin); err != nil {
			problems = append(problems, err)
		}
	}
{%- if cookiecutter.use_postgres == "yes" %}
	if !slices.Contains(RefreshTransports, c.RefreshTransport) {
		problems = append(problems, fmt.Errorf(
			"refreshTransport %q is not one of %v", c.RefreshTransport, RefreshTransports))
	}
	// The service refuses this pair at start-up — its CSRF check only accepts listed origins, so
	// every refresh would fail — and refusing it here turns a deployment that never becomes healthy
	// into an error at preview.
	if c.RefreshTransport == "cookie" && len(c.CORSOrigins) == 0 {
		problems = append(problems, errors.New(
			"refreshTransport cookie requires corsOrigins: the CSRF check only accepts listed origins"))
	}
	if c.DatabaseStorageGB < MinDatabaseStorageGB || c.DatabaseStorageGB >= MaxDatabaseStorageGB {
		problems = append(problems, fmt.Errorf(
			"databaseStorageGb %d is outside %d..%d: gp3's floor, and below the autoscaling ceiling",
			c.DatabaseStorageGB, MinDatabaseStorageGB, MaxDatabaseStorageGB-1))
	}
{%- endif %}
	return errors.Join(problems...)
}

// validateOrigin accepts exactly what a browser sends in an Origin header — a scheme, a host and an
// optional port — by the rule backend/internal/config applies to CORS_ORIGINS, so an entry the
// service would refuse at start-up is refused at preview instead.
func validateOrigin(origin string) error {
	parsed, err := url.Parse(origin)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" ||
		parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" || parsed.User != nil ||
		strings.Contains(origin, "*") {
		return fmt.Errorf("corsOrigins entry %q must be an origin such as https://app.example.com", origin)
	}
	return nil
}

// SplitOrigins parses `corsOrigins`: comma-separated, trimmed, empty entries dropped.
func SplitOrigins(raw string) []string {
	var origins []string
	for _, origin := range strings.Split(raw, ",") {
		if origin = strings.TrimSpace(origin); origin != "" {
			origins = append(origins, origin)
		}
	}
	return origins
}

// SubjectPrefix is what the CI roles' trust policies put before `:environment:…` and
// `:pull_request`: the configured prefix, or the name-only form when none is configured.
func (c StackConfig) SubjectPrefix() string {
	if c.GitHubSubjectPrefix != "" {
		return c.GitHubSubjectPrefix
	}
	return "repo:" + c.GitHubRepository
}

// IsProduction is whether this STACK is the production one. Deletion protection, the final
// snapshot, secret recovery windows and log retention hang off it, and none of them is a question
// about how the application behaves — AppEnvironment is that question.
func (c StackConfig) IsProduction() bool {
	return c.Environment == "prod"
}

// Name builds a resource name that carries the environment, so two stacks can share an account
// safely.
func (c StackConfig) Name(parts ...string) string {
	return strings.Join(append(slices.Clone(parts), c.Environment), "-")
}

// Partition is the ARN partition the region belongs to. Every ARN this stack spells by hand — a
// managed policy, the state bucket, a cluster's services — has it as its second field, and `aws`
// is wrong in GovCloud.
func (c StackConfig) Partition() string {
	if strings.HasPrefix(c.Region, "us-gov-") {
		return "aws-us-gov"
	}
	return "aws"
}

// StateBucket is the Pulumi state bucket infra/bootstrap/state-bucket.sh creates: one per account
// and region, shared by every service deployed there.
func (c StackConfig) StateBucket() string {
	return "pulumi-state-" + c.AccountID + "-" + c.Region
}

// RoleName is an IAM role name for role, trimmed to what IAM accepts.
//
// The slug is the part that gets shortened, because the role and the environment are what make
// two roles different from each other. Shortening silently would be wrong if it could collide,
// and it cannot: the role suffix is unique per role within a stack.
func (c StackConfig) RoleName(role string) (string, error) {
	suffix := "-" + role + "-" + c.Environment
	name := trimHead(c.Slug, MaxRoleName-len(suffix)) + suffix
	if len(name) > MaxRoleName || strings.HasPrefix(name, "-") {
		return "", fmt.Errorf("IAM role name %q does not fit in %d characters", name, MaxRoleName)
	}
	return name, nil
}

// DBIdentifier is the RDS instance identifier, trimmed to what RDS accepts.
func (c StackConfig) DBIdentifier() (string, error) {
	suffix := "-pg-" + c.Environment
	id := trimHead(c.Slug, MaxDBIdentifier-len(suffix)) + suffix
	if len(id) > MaxDBIdentifier || !slugPattern.MatchString(id) || strings.Contains(id, "--") {
		return "", fmt.Errorf("RDS identifier %q is not one RDS accepts", id)
	}
	return id, nil
}

func trimHead(s string, limit int) string {
	if limit < 0 {
		limit = 0
	}
	if len(s) > limit {
		s = s[:limit]
	}
	return strings.TrimRight(s, "-")
}

// Load reads `Pulumi.<stack>.yaml` into a validated StackConfig. It is the only place a
// configuration key is spelled.
func Load(ctx *pulumi.Context) (StackConfig, error) {
	cfg := config.New(ctx, "")

	var missing []error
	require := func(key string) string {
		value, err := cfg.Try(key)
		if err != nil {
			missing = append(missing, fmt.Errorf("%s is required in Pulumi.%s.yaml", key, ctx.Stack()))
		}
		return strings.TrimSpace(value)
	}
	c := Defaults(
		require("accountId"),
		require("region"),
		require("environment"),
		require("githubRepository"),
		require("slug"),
	)
	if err := errors.Join(missing...); err != nil {
		return StackConfig{}, err
	}

	var err error
	if value := cfg.Get("appEnvironment"); value != "" {
		c.AppEnvironment = value
	}
	c.GitHubSubjectPrefix = strings.TrimSpace(cfg.Get("githubSubjectPrefix"))
	if c.MinTasks, err = intOr(cfg, "minTasks", c.MinTasks); err != nil {
		return StackConfig{}, err
	}
	if c.MaxTasks, err = intOr(cfg, "maxTasks", c.MaxTasks); err != nil {
		return StackConfig{}, err
	}
	if value := strings.TrimSpace(cfg.Get("cpu")); value != "" {
		c.CPU = value
	}
	if value := strings.TrimSpace(cfg.Get("memory")); value != "" {
		c.Memory = value
	}
	if c.AvailabilityZoneCount, err = intOr(cfg, "availabilityZoneCount", c.AvailabilityZoneCount); err != nil {
		return StackConfig{}, err
	}
	c.CORSOrigins = SplitOrigins(cfg.Get("corsOrigins"))
{%- if cookiecutter.use_postgres == "yes" %}
	if value := strings.TrimSpace(cfg.Get("refreshTransport")); value != "" {
		c.RefreshTransport = value
	}
	if value := strings.TrimSpace(cfg.Get("databaseInstanceClass")); value != "" {
		c.DatabaseInstanceClass = value
	}
	if c.DatabaseStorageGB, err = intOr(cfg, "databaseStorageGb", c.DatabaseStorageGB); err != nil {
		return StackConfig{}, err
	}
	if c.DatabaseMultiAZ, err = boolOr(cfg, "databaseMultiAz", c.DatabaseMultiAZ); err != nil {
		return StackConfig{}, err
	}
{%- endif %}
{%- if cookiecutter.use_sentry == "yes" %}
	c.SentryDSNVersion = strings.TrimSpace(cfg.Get("sentryDsnVersion"))
{%- endif %}

	return c, c.Validate()
}

// intOr distinguishes "not set" from "set to something that is not a number". The first takes the
// default; the second is an error, because a typo that silently becomes a default is a stack that
// does something nobody configured.
func intOr(cfg *config.Config, key string, fallback int) (int, error) {
	raw := strings.TrimSpace(cfg.Get(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%s: %q is not an integer", key, raw)
	}
	return value, nil
}
{%- if cookiecutter.use_postgres == "yes" %}

func boolOr(cfg *config.Config, key string, fallback bool) (bool, error) {
	raw := strings.TrimSpace(cfg.Get(key))
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		return false, fmt.Errorf("%s: %q is not true or false", key, raw)
	}
	return value, nil
}
{%- endif %}
