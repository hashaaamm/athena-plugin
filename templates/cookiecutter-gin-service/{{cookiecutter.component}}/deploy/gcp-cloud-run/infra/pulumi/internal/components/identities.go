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

// RuntimeRoles are granted to the Cloud Run service.
//
// Secret ACCESS is deliberately absent: it is granted per secret in secrets.go, so a secret added
// later is not automatically readable by everything that can already read these.
var RuntimeRoles = []string{
	"roles/logging.logWriter",
{%- if cookiecutter.use_postgres == "yes" %}
	// The connector's half and the database's half of IAM database authentication: client opens
	// the tunnel to the instance, instanceUser lets the account log in as its own IAM database user.
	// Neither reaches anything a password would.
	"roles/cloudsql.client",
	"roles/cloudsql.instanceUser",
{%- endif %}
}
{%- if cookiecutter.use_postgres == "yes" %}

// MigratorRoles are granted to the migration job's account, which connects through Cloud Run's
// Cloud SQL connection as the `postgres` login. No instanceUser: it does not log in with IAM. Its
// one secret, the admin password, is granted in secrets.go.
var MigratorRoles = []string{
	"roles/logging.logWriter",
	"roles/cloudsql.client",
}
{%- endif %}

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

// ApplyRoles are granted to the apply account, which infra.yml runs `pulumi up` as on a merge to
// main: one predefined admin role per service this stack enables and manages, and read on
// compute, which the provider resolves the project's default region and zone through.
//
// Predefined roles, not `roles/owner`: the GCP infrastructure standards rule out basic roles in
// production. That keeps every grant readable and every service this account can touch named
// here; it does not make the account weak. resourcemanager.projectIamAdmin can grant any role on
// the project, this account's own included, and it has to — the stack binds project roles — so in
// effect the account can do anything in the project. One project per environment is what keeps
// that to the environment it applies. A resource type the stack gains is a role added here, in
// the same pull request, or the apply on main fails with a permission error.
//
// The state bucket and its key are granted on the resources themselves, below, not here.
var ApplyRoles = []string{
	"roles/serviceusage.serviceUsageAdmin",
	"roles/resourcemanager.projectIamAdmin",
	"roles/iam.serviceAccountAdmin",
	"roles/iam.workloadIdentityPoolAdmin",
	"roles/artifactregistry.admin",
	"roles/secretmanager.admin",
	"roles/run.admin",
{%- if cookiecutter.use_postgres == "yes" %}
	"roles/cloudsql.admin",
{%- endif %}
	"roles/compute.viewer",
}

// ApplyEnvironment is the GitHub environment infra.yml's apply job runs in. Only a token from it
// may impersonate the apply account, so the environment's protection rules — required reviewers,
// and main as the only branch that may use it — gate every apply.
const ApplyEnvironment = "infrastructure"

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
}

// Identities is the service accounts and Workload Identity Federation.
//
{%- if cookiecutter.use_postgres == "yes" %}
// Three accounts, and the split is the point: the CI account can build and deploy but cannot read
// the database or a secret's value; the runtime account can read and write rows as its own IAM
// database user, and read its own secrets, but cannot deploy or change the schema; the migrator
// account can change the schema, holding the one database password in the stack, and nothing
// else.
{%- else %}
// Two accounts, and the split is the point: the CI account can build and deploy but cannot read a
// secret's value; the runtime account can read its own secrets but cannot deploy.
{%- endif %}
//
// One more account applies the stack: the APPLY account, which infra.yml runs `pulumi up` as when a
// change merges to main. Only a token from the repository's `infrastructure` GitHub environment may
// impersonate it, and its roles are in ApplyRoles.
//
// No account has a downloadable key. A downloaded service-account JSON key is the most-stolen
// credential class there is, and the house rule is that CI authenticates with Workload Identity
// Federation instead — ask Athena for the Pulumi standards and the GitHub Actions standards.
type Identities struct {
	pulumi.ResourceState

	Runtime  *serviceaccount.Account
	CI       *serviceaccount.Account
	Apply    *serviceaccount.Account
	Pool     *iam.WorkloadIdentityPool
	Provider *iam.WorkloadIdentityPoolProvider
{%- if cookiecutter.use_postgres == "yes" %}

	// Migrator is what the migration job runs as.
	Migrator *serviceaccount.Account
{%- endif %}

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
{%- if cookiecutter.use_postgres == "yes" %}
	if c.Migrator, err = c.account(ctx, cfg, opts, "migrator-sa", "migrate", cfg.Slug+" migrations", MigratorRoles); err != nil {
		return nil, err
	}
{%- endif %}
	if c.CI, err = c.account(ctx, cfg, opts, "ci-sa", "ci", cfg.Slug+" CI", CIRoles); err != nil {
		return nil, err
	}
	ciMember := pulumi.Sprintf("serviceAccount:%s", c.CI.Email)

	// CI deploys revisions that run AS the runtime account, which needs explicit permission to act
	// as it. Without this the deploy fails with a permission error that does not mention
	// impersonation anywhere. Granted per account rather than at the project level:
	// roles/iam.serviceAccountUser on the project is the right to run anything as any account in it.
	if _, err := serviceaccount.NewIAMMember(ctx, "ci-acts-as-runtime", &serviceaccount.IAMMemberArgs{
		ServiceAccountId: c.Runtime.Name,
		Role:             pulumi.String("roles/iam.serviceAccountUser"),
		Member:           ciMember,
	}, child(c, opts)...); err != nil {
		return nil, err
	}
{%- if cookiecutter.use_postgres == "yes" %}
	// The same, for the migration job: CD updates its image, and the job runs as the migrator.
	if _, err := serviceaccount.NewIAMMember(ctx, "ci-acts-as-migrator", &serviceaccount.IAMMemberArgs{
		ServiceAccountId: c.Migrator.Name,
		Role:             pulumi.String("roles/iam.serviceAccountUser"),
		Member:           ciMember,
	}, child(c, opts)...); err != nil {
		return nil, err
	}
{%- endif %}

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
	stateKey := pulumi.String(fmt.Sprintf(
		"projects/%s/locations/%s/keyRings/%s/cryptoKeys/%s", cfg.Project, cfg.Region, Keyring, Key))
	if _, err := kms.NewCryptoKeyIAMMember(ctx, "ci-decrypts-stack-config", &kms.CryptoKeyIAMMemberArgs{
		CryptoKeyId: stateKey,
		Role:        pulumi.String("roles/cloudkms.cryptoKeyDecrypter"),
		Member:      ciMember,
	}, child(c, opts)...); err != nil {
		return nil, err
	}

	// --- The apply account ----------------------------------------------------------------------
	//
	// Every infrastructure change after the first is applied by infra.yml on a merge to main, as
	// this account (Pulumi standards MUST-9). The first apply runs from an operator's machine,
	// because it is the apply that creates this account.
	if c.Apply, err = c.account(ctx, cfg, opts, "apply-sa", "apply", cfg.Slug+" apply", ApplyRoles); err != nil {
		return nil, err
	}
	applyMember := pulumi.Sprintf("serviceAccount:%s", c.Apply.Email)

	// The apply creates and updates Cloud Run resources that run as these accounts, which needs
	// the right to act as each — granted per account, as for CI.
	actsAs := map[string]*serviceaccount.Account{"apply-acts-as-runtime": c.Runtime}
{%- if cookiecutter.use_postgres == "yes" %}
	actsAs["apply-acts-as-migrator"] = c.Migrator
{%- endif %}
	for name, account := range actsAs {
		if _, err := serviceaccount.NewIAMMember(ctx, name, &serviceaccount.IAMMemberArgs{
			ServiceAccountId: account.Name,
			Role:             pulumi.String("roles/iam.serviceAccountUser"),
			Member:           applyMember,
		}, child(c, opts)...); err != nil {
			return nil, err
		}
	}

	// The apply writes the state, and the CI account's grants on the state bucket and its key are
	// resources of this stack, so it administers both — on those two resources, not the project.
	// Encrypter as well as decrypter: an apply writes the stack's secret outputs back into state.
	if _, err := storage.NewBucketIAMMember(ctx, "apply-administers-pulumi-state", &storage.BucketIAMMemberArgs{
		Bucket: pulumi.String(cfg.Project + StateBucketSuffix),
		Role:   pulumi.String("roles/storage.admin"),
		Member: applyMember,
	}, child(c, opts)...); err != nil {
		return nil, err
	}
	for name, role := range map[string]string{
		"apply-administers-state-key": "roles/cloudkms.admin",
		"apply-uses-state-key":        "roles/cloudkms.cryptoKeyEncrypterDecrypter",
	} {
		if _, err := kms.NewCryptoKeyIAMMember(ctx, name, &kms.CryptoKeyIAMMemberArgs{
			CryptoKeyId: stateKey,
			Role:        pulumi.String(role),
			Member:      applyMember,
		}, child(c, opts)...); err != nil {
			return nil, err
		}
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
			// The GitHub environment a job runs in; the claim is present only when the job names
			// one. The apply account is impersonable from the `infrastructure` environment alone.
			"attribute.environment": pulumi.String("assertion.environment"),
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

	// Only infra.yml's apply job may impersonate the apply account: a token from this repository —
	// the provider's attribute condition — whose job runs in the `infrastructure` environment.
	// A pull request's preview, the deploy job and every other workflow get a token from another
	// environment or none, and are refused.
	if _, err := serviceaccount.NewIAMMember(ctx, "github-impersonates-apply", &serviceaccount.IAMMemberArgs{
		ServiceAccountId: c.Apply.Name,
		Role:             pulumi.String("roles/iam.workloadIdentityUser"),
		Member: pulumi.Sprintf(
			"principalSet://iam.googleapis.com/projects/%s/locations/global/workloadIdentityPools/%s/attribute.environment/%s",
			args.ProjectNumber, c.Pool.WorkloadIdentityPoolId, ApplyEnvironment),
	}, child(c, opts)...); err != nil {
		return nil, err
	}

	c.ProviderName = c.Provider.Name

	return c, ctx.RegisterResourceOutputs(c, pulumi.Map{
		"runtimeEmail": c.Runtime.Email,
		"ciEmail":      c.CI.Email,
		"applyEmail":   c.Apply.Email,
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
