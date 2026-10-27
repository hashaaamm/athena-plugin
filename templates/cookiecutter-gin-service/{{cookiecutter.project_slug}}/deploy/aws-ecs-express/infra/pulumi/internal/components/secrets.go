package components

import (
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/secretsmanager"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"{{ cookiecutter.go_module }}/infra/pulumi/internal/stackconfig"
)

// SecretNames is every Secrets Manager secret the stack creates, each of them empty.
{%- if cookiecutter.use_postgres == "yes" or cookiecutter.use_sentry == "yes" %}
var SecretNames = []string{
{%- if cookiecutter.use_postgres == "yes" %}
	// The HS256 key the service signs access tokens with. `just infra-secrets`.
	"jwt-secret",
{%- endif %}
{%- if cookiecutter.use_sentry == "yes" %}
	// Sentry's project DSN, which originates in Sentry. `aws secretsmanager put-secret-value` by hand.
	"sentry-dsn",
{%- endif %}
}
{%- else %}
var SecretNames = []string{}
{%- endif %}

// RecoveryWindowDays is how long a deleted production secret stays restorable. Outside production
// the window is zero — deleted at once — because Secrets Manager keeps the NAME reserved for the
// whole window, and a `pulumi destroy` followed by a `pulumi up` then fails on a secret that is
// "scheduled for deletion" and cannot be recreated for a month.
const RecoveryWindowDays = 30

// SecretRef is what a container needs to mount a secret: the secret, and the exact version — or,
// for a JSON secret, the one key — to read.
type SecretRef struct {
	ARN pulumi.StringInput
	// VersionID pins the version. Nil only for RDSManagedKey, the one deliberate exception.
	VersionID pulumi.StringInput
	// JSONKey selects one key of a JSON secret. Empty means the whole secret string.
	JSONKey string
}

// RDSManagedKey is one key of the master-user secret RDS manages — `username` or `password` — and
// it is the one reference in this stack that does NOT pin a version.
//
// The exception is deliberate (ADR-0023). Every other secret here is the stack's: a new value is a
// new version a human records, and pinning makes it a deploy. This one is RDS's: RDS creates it,
// and RDS rotates it on its own schedule, retiring the old password as it goes. A pinned version
// would be a password the database no longer accepts after the first rotation, and the migration
// task would stop being able to log in. Unpinned, ECS resolves the current version when the task
// STARTS, so a rotation affects only the next migration run, never one in progress.
func RDSManagedKey(arn pulumi.StringInput, key string) SecretRef {
	return SecretRef{ARN: arn, JSONKey: key}
}

// ValueFrom is the reference ECS resolves: `<secret-arn>:<json-key>:<version-stage>:<version-id>`.
// The stage is always empty, and so is the key for a whole-string secret, which is read at one
// VersionId — never the AWSCURRENT stage implicitly. ADR-0016's rule, applied to ECS: a task reads
// its secrets when it STARTS, so a stage-relative reference lets two tasks of one deployment hold
// different values after a rotation. Pinned, a new value is a new task definition — a deploy — and
// a rollback restores the credential with the image.
func (r SecretRef) ValueFrom() pulumi.StringOutput {
	if r.VersionID == nil {
		return pulumi.Sprintf("%s:%s::", r.ARN, r.JSONKey)
	}
	return pulumi.Sprintf("%s:%s::%s", r.ARN, r.JSONKey, r.VersionID)
}

// SecretsArgs is what Secrets needs beyond the configuration.
type SecretsArgs struct {
	// Versions maps a name in SecretNames to the VersionId its value was put as. An empty or absent
	// entry means "not populated yet", and the secret gets no reference.
	Versions map[string]string
}

// Secrets is the stack's Secrets Manager secrets.
//
// The house rule is that secret VALUES never live in infrastructure code, in a stack configuration
// file, or in state — ask Athena for the Pulumi standards (MUST-3), and see ADR-0023 for how this
// stack meets it. So this component creates secrets and never a version: a value is put out of
// band — by `just infra-secrets` for the signing key, by a human for anything that originates
// somewhere else — and its VersionId, which is not a secret, goes into stack configuration. Until
// then a secret has no reference, so nothing can mount it, and mounting an empty secret is an error
// at plan time rather than a task that cannot start.
//
// The database's master password is not here at all: RDS creates and keeps that secret itself. See
// Database.
//
// Encrypted with the account's `aws/secretsmanager` key. A customer-managed key would add a key
// policy that has to name the execution role, for no protection the default key lacks here: the
// IAM grant on each secret is the boundary.
type Secrets struct {
	pulumi.ResourceState

	// ARNs maps a logical name to its secret's ARN — every secret, populated or not. The service's
	// execution role is granted exactly these.
	ARNs map[string]pulumi.StringOutput

	// Refs maps a logical name to a pinned reference, for every secret whose VersionId is in stack
	// configuration.
	Refs map[string]SecretRef
}

// NewSecrets creates every secret, empty.
func NewSecrets(
	ctx *pulumi.Context, cfg stackconfig.StackConfig, args SecretsArgs, opts ...pulumi.ResourceOption,
) (*Secrets, error) {
	c := &Secrets{
		ARNs: map[string]pulumi.StringOutput{},
		Refs: map[string]SecretRef{},
	}
	if err := ctx.RegisterComponentResource("service:infra:Secrets", cfg.Name("secrets"), c, opts...); err != nil {
		return nil, err
	}

	window := 0
	if cfg.IsProduction() {
		window = RecoveryWindowDays
	}
	for _, name := range SecretNames {
		secret, err := secretsmanager.NewSecret(ctx, name, &secretsmanager.SecretArgs{
			Name:                 pulumi.String(cfg.Name(cfg.Slug, name)),
			RecoveryWindowInDays: pulumi.Int(window),
		}, child(c, opts)...)
		if err != nil {
			return nil, err
		}
		c.ARNs[name] = secret.Arn
		if version := args.Versions[name]; version != "" {
			c.Refs[name] = SecretRef{ARN: secret.Arn, VersionID: pulumi.String(version)}
		}
	}

	return c, ctx.RegisterResourceOutputs(c, pulumi.Map{})
}
