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
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi/config"
)

const (
	// MaxAccountID is GCP's upper bound on a service-account id (6-30 characters). The environment
	// suffix and the role suffix are fixed-width, so the slug is what has to give when a name is
	// long.
	MaxAccountID = 30

	// MaxPoolID is the upper bound on a Workload Identity Pool id (4-32 characters), and the same
	// squeeze applies.
	MaxPoolID = 32

	// disambiguator is the number of digest characters appended to a pool id. Four is 65,536
	// values, which is not a cryptographic guarantee and does not need to be: the thing it
	// separates is two services a human deliberately named in one project, not an adversary
	// hunting a collision.
	disambiguator = 4

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

// StackConfig is every value the stack reads, resolved and validated.
type StackConfig struct {
	Project string
	Region  string

	// Environment is the STACK's environment. It names resources — `api-dev`, and every account,
	// secret and repository with the same suffix — and decides IsProduction. The application has
	// never heard of it.
	Environment string

	// GitHubRepository is `owner/repo`. The Workload Identity provider's attribute condition is
	// built from it, and it is the single line that stops any repository on GitHub from assuming
	// the CI identity.
	GitHubRepository string

	// Slug is the base name every resource is derived from: the repository's slug and the
	// component's name, e.g. `billing-api`. Two components in one project then never share a
	// resource name.
	Slug string

	// ServiceImage is the image the Cloud Run service and its migration job are created on — the
	// image `just infra-first-deploy` built and pushed, and recorded here. Empty, the default,
	// means the stack creates everything else and no Cloud Run service at all: there is no placeholder
	// image, because a service running somebody else's container proves nothing and has to be
	// replaced anyway. Once it is set the pipeline owns the image (ignoreChanges and --refresh, see
	// components.Service), and this value is only what the Cloud Run service was first created with.
	ServiceImage string

	// AppEnvironment is what the APPLICATION is told it is running as, which is a different
	// question from what this stack is called. It becomes ENVIRONMENT on the Cloud Run revision
	// and decides how the service behaves: whether /docs and /openapi.json are served, whether a
	// placeholder database password is refused.
	//
	// They are set separately because they vary separately. A `dev` stack that anyone outside the
	// team can reach is `production` to the application; a `prod` stack standing in for a
	// customer demo is not. Set it to `production` in the stack real users reach. See
	// infra/README.md.
	AppEnvironment string

	MinInstances int
	MaxInstances int

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

	// DatabaseTier and DatabaseDiskGB are already the cheapest Cloud SQL settings that work;
	// infra/README.md has the table of why.
	DatabaseTier   string
	DatabaseDiskGB int

	// JWTSecretVersion is the Secret Manager version of the token signing key the service pins,
	// and DBAdminPasswordVersion the version of the `postgres` login's password the migration job
	// pins. Empty means "not mounted".
	//
	// Neither value is ever in this program, its configuration or its state: Pulumi standards
	// MUST-3, and ADR-0023 for how this stack meets it. The stack creates both containers empty;
	// `just infra-secrets` generates each value, adds it as a version, sets the password on the
	// Cloud SQL login, and records the version numbers here. Until both are set the stack mounts
	// neither, and secrets_ready says so — which is what sync-github.sh checks before it lets CD
	// deploy an image that would refuse to start without its key.
	JWTSecretVersion       string
	DBAdminPasswordVersion string
{%- endif %}
{%- if cookiecutter.use_sentry == "yes" %}

	// SentryDSNVersion is the Sentry DSN secret version the service pins, e.g. "1". Empty means
	// "not mounted".
	//
	// Empty by default, and the default has to stay applyable: mounting a container with no
	// versions is a deploy that fails outright rather than a service that starts without error
	// tracking. The stack creates the empty container; a human runs `gcloud secrets versions add`
	// and puts the number it prints here; only then does the mount appear. Rotating the DSN is
	// the same two steps with the next number, which deploys a new revision.
	SentryDSNVersion string
{%- endif %}
}

// Defaults returns a configuration holding the required values and every default. Load starts
// from it, and so do the tests, so a default is written down exactly once.
func Defaults(project, region, environment, githubRepository, slug string) StackConfig {
	c := StackConfig{
		Project:          project,
		Region:           region,
		Environment:      environment,
		GitHubRepository: githubRepository,
		Slug:             slug,
		AppEnvironment:   DefaultAppEnvironment,
		// Scaling to zero is the single biggest cost lever: an idle service costs nothing.
		MinInstances: 0,
		MaxInstances: 4,
	}
{%- if cookiecutter.use_postgres == "yes" %}
	c.RefreshTransport = "body"
	c.DatabaseTier = "db-f1-micro"
	c.DatabaseDiskGB = 10
{%- endif %}
	return c
}

// Validate refuses a configuration the stack or the application cannot work with, at load time,
// rather than letting it surface as a resource GCP rejects half way through an apply.
func (c StackConfig) Validate() error {
	var problems []error
	required := [][2]string{
		{"project", c.Project},
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
	// A reference, not a placeholder: no whitespace, and a tag or a digest. `latest` is refused for
	// the reason CD never pushes it — a rollback has to be able to name what it rolls back to.
	if c.ServiceImage != "" && !pinnedImage(c.ServiceImage) {
		problems = append(problems, fmt.Errorf(
			"serviceImage %q is not an image reference with a tag other than latest, or a digest, "+
				"such as <image_repo>:<git sha>", c.ServiceImage))
	}
	if !slices.Contains(AppEnvironments, c.AppEnvironment) {
		problems = append(problems, fmt.Errorf(
			"appEnvironment %q is not one of %v", c.AppEnvironment, AppEnvironments))
	}
	// One slash, two non-empty halves. Anything else builds an attribute condition that matches
	// nothing — or, worse, something nobody meant.
	if owner, repo, ok := strings.Cut(c.GitHubRepository, "/"); !ok || owner == "" || repo == "" ||
		strings.Contains(repo, "/") {
		problems = append(problems, fmt.Errorf(
			"githubRepository %q is not owner/repo", c.GitHubRepository))
	}
	if c.MinInstances < 0 || c.MaxInstances < 1 || c.MinInstances > c.MaxInstances {
		problems = append(problems, fmt.Errorf(
			"minInstances %d / maxInstances %d is not a valid range", c.MinInstances, c.MaxInstances))
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
	// every refresh would fail — and refusing it here turns a revision that never becomes ready
	// into an error at preview.
	if c.RefreshTransport == "cookie" && len(c.CORSOrigins) == 0 {
		problems = append(problems, errors.New(
			"refreshTransport cookie requires corsOrigins: the CSRF check only accepts listed origins"))
	}
	// 10 GB is Cloud SQL's minimum instance storage; GCP refuses anything smaller mid-apply.
	if c.DatabaseDiskGB < 10 {
		problems = append(problems, fmt.Errorf(
			"databaseDiskGb %d is below Cloud SQL's 10 GB minimum", c.DatabaseDiskGB))
	}
{%- endif %}
	return errors.Join(problems...)
}

// pinnedImage is whether ref names one image: a repository with a tag other than `latest`, or a
// digest, and no whitespace.
func pinnedImage(ref string) bool {
	if ref == "" || strings.ContainsAny(ref, " \t\n") {
		return false
	}
	if repo, digest, ok := strings.Cut(ref, "@"); ok {
		return repo != "" && strings.HasPrefix(digest, "sha256:")
	}
	// The tag follows the last colon after the last slash; a colon before it is a registry port.
	name := ref[strings.LastIndex(ref, "/")+1:]
	_, tag, ok := strings.Cut(name, ":")
	return ok && tag != "" && tag != "latest"
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

// SecretsReady is whether every secret the service cannot start without has a pinned version —
// the stack's secrets_ready export. A Sentry DSN is optional and does not count.
func (c StackConfig) SecretsReady() bool {
{%- if cookiecutter.use_postgres == "yes" %}
	return c.JWTSecretVersion != "" && c.DBAdminPasswordVersion != ""
{%- else %}
	// Nothing to generate: no database means no users, so no signing key.
	return true
{%- endif %}
}

// IsProduction is whether this STACK is the production one. Deletion protection and
// point-in-time recovery hang off it, and neither is a question about how the application
// behaves — AppEnvironment is that question.
func (c StackConfig) IsProduction() bool {
	return c.Environment == "prod"
}

// Name builds a resource name that carries the environment, so two stacks can share a project
// safely.
func (c StackConfig) Name(parts ...string) string {
	return strings.Join(append(slices.Clone(parts), c.Environment), "-")
}

// AccountID is a service-account id for role, trimmed to what GCP accepts.
//
// The slug is the part that gets shortened, because the role and the environment are what make
// two accounts different from each other. Shortening silently would be wrong if it could collide,
// and it cannot: the role suffix is unique per account within a stack.
func (c StackConfig) AccountID(role string) (string, error) {
	suffix := "-" + role + "-" + c.Environment
	account := trimHead(c.Slug, MaxAccountID-len(suffix)) + suffix
	if len(account) < 6 || len(account) > MaxAccountID {
		return "", fmt.Errorf("service account id %q is not 6-30 characters", account)
	}
	return account, nil
}

// PoolID is a Workload Identity Pool id, unique within the PROJECT rather than the stack.
//
// Name is not enough here, and the difference between them is a 409 on somebody else's resource.
// A pool id is unique across a GCP project, and two services generated from this template into the
// same project both want one — so an id built from the kind and the environment alone is already
// taken the second time, by a pool this stack does not own and must not adopt.
//
// Deleting a pool to free its name does not help either: GCP keeps it for 30 days before the id
// can be reused, which turns a rename into a month of waiting. Get it right the first time.
//
// The trailing digest is what makes "unique" true rather than usually-true. 32 characters is not
// many, so a long slug gets trimmed, and trimming is exactly where two distinct services become
// one name: `platform-billing-service-api` and `platform-billing-service-web` both cut down to
// `platform-billing-serv`. The digest is taken over the untrimmed slug and the repository, so what
// the trim throws away is still represented in the result.
func (c StackConfig) PoolID(kind string) (string, error) {
	suffix := "-" + kind + "-" + c.Environment + "-" + digest(c.Slug, c.GitHubRepository)
	pool := trimHead(c.Slug, MaxPoolID-len(suffix)) + suffix
	if len(pool) < 4 || len(pool) > MaxPoolID {
		return "", fmt.Errorf("workload identity pool id %q is not 4-32 characters", pool)
	}
	if strings.HasPrefix(pool, "gcp-") {
		return "", fmt.Errorf("workload identity pool id %q may not start with 'gcp-'", pool)
	}
	return pool, nil
}

// RegistryHost is the Artifact Registry host for the stack's region.
func (c StackConfig) RegistryHost() string {
	return c.Region + "-docker.pkg.dev"
}

// digest is a stable short id for a name, derived from everything that makes it that name.
//
// Deterministic, and that is the whole requirement. A random suffix would be more unique and
// would also be unusable: Pulumi would see a different pool id on every run, replace the pool
// every time, and each replacement puts the old id into GCP's 30-day hold.
func digest(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:])[:disambiguator]
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
		return value
	}
	c := Defaults(
		require("project"),
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
	c.ServiceImage = strings.TrimSpace(cfg.Get("serviceImage"))
	if c.MinInstances, err = intOr(cfg, "minInstances", c.MinInstances); err != nil {
		return StackConfig{}, err
	}
	if c.MaxInstances, err = intOr(cfg, "maxInstances", c.MaxInstances); err != nil {
		return StackConfig{}, err
	}
	c.CORSOrigins = SplitOrigins(cfg.Get("corsOrigins"))
{%- if cookiecutter.use_postgres == "yes" %}
	if value := strings.TrimSpace(cfg.Get("refreshTransport")); value != "" {
		c.RefreshTransport = value
	}
	if value := cfg.Get("databaseTier"); value != "" {
		c.DatabaseTier = value
	}
	if c.DatabaseDiskGB, err = intOr(cfg, "databaseDiskGb", c.DatabaseDiskGB); err != nil {
		return StackConfig{}, err
	}
	c.JWTSecretVersion = strings.TrimSpace(cfg.Get("jwtSecretVersion"))
	c.DBAdminPasswordVersion = strings.TrimSpace(cfg.Get("dbAdminPasswordVersion"))
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
