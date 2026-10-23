package components

import (
	"fmt"
	"strings"

	"github.com/pulumi/pulumi-gcp/sdk/v10/go/gcp/iam"
	"github.com/pulumi/pulumi-gcp/sdk/v10/go/gcp/kms"
	"github.com/pulumi/pulumi-gcp/sdk/v10/go/gcp/projects"
	"github.com/pulumi/pulumi-gcp/sdk/v10/go/gcp/serviceaccount"
	"github.com/pulumi/pulumi-gcp/sdk/v10/go/gcp/storage"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"{{ cookiecutter.go_module }}/infra/pulumi/internal/stackconfig"
)

// RuntimeRoles are granted to the Cloud Run service and its jobs.
//
// Secret ACCESS is deliberately absent: it is granted per secret in secrets.go, so a secret added
// later is not automatically readable by everything that can already read these.
var RuntimeRoles = []string{
	"roles/logging.logWriter",
{%- if cookiecutter.use_postgres == "yes" %}
	"roles/cloudsql.client",
{%- endif %}
}

// WebRuntimeRoles are granted to the Cloud Run service that serves a built frontend bundle, when
// frontendEnabled is on.
//
// Its own account rather than the backend's, and the list is short because nginx serving static
// files reads no database and no secret. Sharing the backend's account would hand the least
// protected surface in the system — a public web server with no application code in it — the
// backend's Cloud SQL client role and whatever secret grants it accumulates.
var WebRuntimeRoles = []string{
	"roles/logging.logWriter",
}

// CIRoles are granted to the GitHub Actions identity.
//
// `viewer` and `iam.securityReviewer` are there for one job: the `pulumi preview` a pull request
// runs. A preview runs with --refresh, so it reads every resource in the stack, and refreshing an
// IAM binding means reading an IAM policy — which `viewer` deliberately excludes. Granting the
// individual read roles instead is the wrong shape: a hand-maintained list breaks every time the
// stack gains a resource type.
//
// What this widens, stated plainly: CI can read resource metadata across the project rather than
// only what it deploys. What it does NOT widen is what matters — both roles are read-only, and
// reading a secret's value needs secretmanager.versions.access, which neither carries.
var CIRoles = []string{
	"roles/run.developer",
	"roles/artifactregistry.writer",
	"roles/viewer",
	"roles/iam.securityReviewer",
}

// The bucket the bootstrap script creates, and the KMS key it creates alongside. Named here rather
// than passed in, because the bootstrap script names them the same way and the two must agree — a
// test reads both scripts and checks.
const (
	StateBucketSuffix = "-pulumi-state"
	Keyring           = "pulumi"
	Key               = "stack"
)

// GitHubIssuer is the OIDC issuer GitHub Actions tokens are signed by.
const GitHubIssuer = "https://token.actions.githubusercontent.com"

// IdentitiesArgs is what Identities needs beyond the configuration.
type IdentitiesArgs struct {
	// ProjectNumber is the numeric project id the Workload Identity principal set is built from.
	ProjectNumber pulumi.StringInput

	// Frontend adds the frontend's runtime account. It follows StackConfig.FrontendEnabled.
	Frontend bool
}

// Identities is the service accounts and Workload Identity Federation.
//
// Two accounts, and the split is the point: the CI account can build and deploy but cannot read
// the database or a secret's value; the runtime account can read its own secrets but cannot
// deploy. Neither has a downloadable key. A downloaded service-account JSON key is the most-stolen
// credential class there is, and the house rule is that CI authenticates with Workload Identity
// Federation instead — ask Athena for the Pulumi standards and the GitHub Actions standards.
type Identities struct {
	pulumi.ResourceState

	Runtime *serviceaccount.Account
	// WebRuntime is nil unless IdentitiesArgs.Frontend is set.
	WebRuntime *serviceaccount.Account
	CI         *serviceaccount.Account
	Pool       *iam.WorkloadIdentityPool
	Provider   *iam.WorkloadIdentityPoolProvider

	// ProviderName is the value GitHub Actions needs as the WIF_PROVIDER repository variable.
	ProviderName pulumi.StringOutput
}

// NewIdentities creates the accounts, their roles, and the Workload Identity pool CI uses.
func NewIdentities(
	ctx *pulumi.Context, cfg stackconfig.StackConfig, args IdentitiesArgs, opts ...pulumi.ResourceOption,
) (*Identities, error) {
	c := &Identities{}
	if err := ctx.RegisterComponentResource("service:infra:Identities", cfg.Name("identities"), c, opts...); err != nil {
		return nil, err
	}

	var err error
	if c.Runtime, err = c.account(ctx, cfg, opts, "runtime-sa", "run", cfg.Slug+" runtime", RuntimeRoles); err != nil {
		return nil, err
	}
	if args.Frontend {
		c.WebRuntime, err = c.account(ctx, cfg, opts, "web-runtime-sa", "web", cfg.Slug+" frontend runtime", WebRuntimeRoles)
		if err != nil {
			return nil, err
		}
	}
	if c.CI, err = c.account(ctx, cfg, opts, "ci-sa", "ci", cfg.Slug+" CI", CIRoles); err != nil {
		return nil, err
	}
	ciMember := pulumi.Sprintf("serviceAccount:%s", c.CI.Email)

	// CI deploys revisions that run AS the runtime account, which needs explicit permission to act
	// as it. Without this the deploy fails with a permission error that does not mention
	// impersonation anywhere. Granted per account rather than at the project level:
	// roles/iam.serviceAccountUser on the project is the right to run anything as any account in it.
	actAs := map[string]*serviceaccount.Account{"ci-acts-as-runtime": c.Runtime}
	if c.WebRuntime != nil {
		actAs["ci-acts-as-web-runtime"] = c.WebRuntime
	}
	for _, name := range sortedKeys(actAs) {
		if _, err := serviceaccount.NewIAMMember(ctx, name, &serviceaccount.IAMMemberArgs{
			ServiceAccountId: actAs[name].Name,
			Role:             pulumi.String("roles/iam.serviceAccountUser"),
			Member:           ciMember,
		}, child(c, opts)...); err != nil {
			return nil, err
		}
	}

	// Read and write the Pulumi state bucket, so the preview job on a pull request can select the
	// stack. Bucket-scoped rather than a project storage role, because a project-level storage role
	// is access to every bucket in the project — including the registry's.
	//
	// objectUser rather than objectViewer: the self-managed backend writes a lock object, so a
	// read-only grant fails on the lock rather than on the listing, which is the worse failure
	// because it looks like a Pulumi bug rather than a missing permission.
	//
	// The bucket is created by the bootstrap script, not here — Pulumi cannot create the bucket it
	// stores its own state in. The binding lives here because the account it grants to does not
	// exist until Pulumi makes it.
	if _, err := storage.NewBucketIAMMember(ctx, "ci-reads-pulumi-state", &storage.BucketIAMMemberArgs{
		Bucket: pulumi.String(cfg.Project + StateBucketSuffix),
		Role:   pulumi.String("roles/storage.objectUser"),
		Member: ciMember,
	}, child(c, opts)...); err != nil {
		return nil, err
	}

	// Reading the state is not enough to read the STACK. Stack configuration is encrypted with the
	// KMS key named by `secretsprovider`, so a preview that can list the bucket then fails on
	// cloudkms.cryptoKeyVersions.useToDecrypt — one permission further in, with an error that names
	// KMS rather than the missing grant. Decrypter and not encrypter: a preview reads configuration
	// and never writes any.
	if _, err := kms.NewCryptoKeyIAMMember(ctx, "ci-decrypts-stack-config", &kms.CryptoKeyIAMMemberArgs{
		CryptoKeyId: pulumi.String(fmt.Sprintf(
			"projects/%s/locations/%s/keyRings/%s/cryptoKeys/%s", cfg.Project, cfg.Region, Keyring, Key)),
		Role:   pulumi.String("roles/cloudkms.cryptoKeyDecrypter"),
		Member: ciMember,
	}, child(c, opts)...); err != nil {
		return nil, err
	}

	poolID, err := cfg.PoolID("github")
	if err != nil {
		return nil, err
	}
	if c.Pool, err = iam.NewWorkloadIdentityPool(ctx, "github-pool", &iam.WorkloadIdentityPoolArgs{
		Project:                pulumi.String(cfg.Project),
		WorkloadIdentityPoolId: pulumi.String(poolID),
		DisplayName:            pulumi.String("GitHub Actions"),
	}, child(c, opts)...); err != nil {
		return nil, err
	}

	if c.Provider, err = iam.NewWorkloadIdentityPoolProvider(ctx, "github-provider", &iam.WorkloadIdentityPoolProviderArgs{
		Project:                        pulumi.String(cfg.Project),
		WorkloadIdentityPoolId:         c.Pool.WorkloadIdentityPoolId,
		WorkloadIdentityPoolProviderId: pulumi.String("github"),
		DisplayName:                    pulumi.String("GitHub OIDC"),
		AttributeMapping: pulumi.StringMap{
			"google.subject":       pulumi.String("assertion.sub"),
			"attribute.repository": pulumi.String("assertion.repository"),
			"attribute.ref":        pulumi.String("assertion.ref"),
		},
		// Without this condition, ANY GitHub repository in the world could mint a token for this
		// pool. It is the single most important line in this file.
		AttributeCondition: pulumi.String(AttributeCondition(cfg.GitHubRepository)),
		Oidc: &iam.WorkloadIdentityPoolProviderOidcArgs{
			IssuerUri: pulumi.String(GitHubIssuer),
		},
	}, child(c, opts)...); err != nil {
		return nil, err
	}

	// Only workflows in that repository may impersonate the CI account.
	if _, err := serviceaccount.NewIAMMember(ctx, "github-impersonates-ci", &serviceaccount.IAMMemberArgs{
		ServiceAccountId: c.CI.Name,
		Role:             pulumi.String("roles/iam.workloadIdentityUser"),
		Member: pulumi.Sprintf(
			"principalSet://iam.googleapis.com/projects/%s/locations/global/workloadIdentityPools/%s/attribute.repository/%s",
			args.ProjectNumber, c.Pool.WorkloadIdentityPoolId, cfg.GitHubRepository),
	}, child(c, opts)...); err != nil {
		return nil, err
	}

	c.ProviderName = c.Provider.Name

	return c, ctx.RegisterResourceOutputs(c, pulumi.Map{
		"runtimeEmail": c.Runtime.Email,
		"ciEmail":      c.CI.Email,
	})
}

// AttributeCondition is the CEL expression that admits tokens from exactly one repository.
func AttributeCondition(repository string) string {
	return fmt.Sprintf("assertion.repository == %q", repository)
}

// account creates a service account and binds its project roles.
func (c *Identities) account(
	ctx *pulumi.Context, cfg stackconfig.StackConfig, opts []pulumi.ResourceOption,
	name, role, display string, roles []string,
) (*serviceaccount.Account, error) {
	accountID, err := cfg.AccountID(role)
	if err != nil {
		return nil, err
	}
	account, err := serviceaccount.NewAccount(ctx, name, &serviceaccount.AccountArgs{
		Project:     pulumi.String(cfg.Project),
		AccountId:   pulumi.String(accountID),
		DisplayName: pulumi.String(display),
	}, child(c, opts)...)
	if err != nil {
		return nil, err
	}
	label := strings.TrimSuffix(name, "-sa")
	member := pulumi.Sprintf("serviceAccount:%s", account.Email)
	for _, grant := range roles {
		if _, err := projects.NewIAMMember(ctx, label+"-"+grant[strings.LastIndex(grant, "/")+1:], &projects.IAMMemberArgs{
			Project: pulumi.String(cfg.Project),
			Role:    pulumi.String(grant),
			Member:  member,
		}, child(c, opts)...); err != nil {
			return nil, err
		}
	}
	return account, nil
}
