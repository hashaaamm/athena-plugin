package components

import (
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/secretsmanager"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"{{ cookiecutter.go_module }}/infra/pulumi/internal/stackconfig"
)

// ManualSecrets are secrets created empty, for values that originate outside this stack. A human
// populates them; nothing mounts them until their VersionId is in stack configuration.
{%- if cookiecutter.use_sentry == "yes" %}
var ManualSecrets = []string{"sentry-dsn"}
{%- else %}
var ManualSecrets = []string{}
{%- endif %}

// RecoveryWindowDays is how long a deleted production secret stays restorable. Outside production
// the window is zero — deleted at once — because Secrets Manager keeps the NAME reserved for the
// whole window, and a `pulumi destroy` followed by a `pulumi up` then fails on a secret that is
// "scheduled for deletion" and cannot be recreated for a month.
const RecoveryWindowDays = 30

// SecretRef is what a container needs to mount a secret: the secret and the exact version to read.
type SecretRef struct {
	ARN       pulumi.StringInput
	VersionID pulumi.StringInput
}

// ValueFrom is the reference ECS resolves: `<secret-arn>:<json-key>:<version-stage>:<version-id>`,
// with the JSON key and the stage left empty. The value is the whole secret string, read at one
// VersionId — never the AWSCURRENT stage implicitly. ADR-0016's rule, applied to ECS: a task reads
// its secrets when it STARTS, so a stage-relative reference lets two tasks of one deployment hold
// different values after a rotation. Pinned, a new value is a new task definition — a deploy — and
// a rollback restores the credential with the image.
func (r SecretRef) ValueFrom() pulumi.StringOutput {
	return pulumi.Sprintf("%s:::%s", r.ARN, r.VersionID)
}

// SecretsArgs is what Secrets needs beyond the configuration.
type SecretsArgs struct {
	// Generated maps a logical name to a value this stack mints and no human ever reads. Each gets
	// its secret and its first version.
	Generated map[string]pulumi.StringInput

	// ManualVersions maps a name in ManualSecrets to the VersionId a human's put-secret-value
	// printed. An empty or absent entry means "not populated yet", and the secret gets no
	// reference.
	ManualVersions map[string]string
}

// Secrets is the stack's Secrets Manager secrets.
//
// The house rule is that secret VALUES never live in infrastructure code, in a stack configuration
// file, or in state — ask Athena for the Pulumi standards. What that protects against is a
// credential a human chose, typed somewhere, and can leak. It is not a prohibition on the stack
// generating a credential nothing else ever needs to know.
//
// So there are two kinds of secret here, and they behave differently:
//
//   - Generated. Values this stack mints and no human ever reads — with a database, the DSN and
//     the token signing key. They are encrypted in state with the KMS key the bootstrap script
//     created, and they have a version the moment the stack applies, which the service pins by
//     VersionId.
//   - Manual. Anything that genuinely originates outside — a Sentry DSN, a third-party API key.
//     The stack creates the empty secret; a human adds the value with
//     `aws secretsmanager put-secret-value` and puts the VersionId it prints into stack
//     configuration. Until then the secret has no reference, so nothing can mount it.
//
// Encrypted with the account's `aws/secretsmanager` key. A customer-managed key would add a key
// policy that has to name the execution role, for no protection the default key lacks here: the
// IAM grant on each secret is the boundary.
type Secrets struct {
	pulumi.ResourceState

	// ARNs maps a logical name to its secret's ARN — every secret, populated or not. The
	// execution role is granted exactly these.
	ARNs map[string]pulumi.StringOutput

	// GeneratedARNs is the subset whose value the stack itself writes. The preview role may read
	// these and no others; see Identities.
	GeneratedARNs []pulumi.StringOutput

	// Versions is every version this component creates. Anything that mounts a secret must depend
	// on these, not merely on the secrets — the secret is not the dependency, the value is. A
	// task that starts before the version exists fails to resolve it and never starts.
	Versions []pulumi.Resource

	// Refs maps a logical name to a pinned reference, for every secret that has a version to pin.
	// A manual secret appears here only once its VersionId is in stack configuration, so mounting
	// an empty secret is an error at plan time rather than a task that cannot start.
	Refs map[string]SecretRef
}

// NewSecrets creates every secret and the generated versions.
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

	// Sorted, so two runs register resources in the same order and a preview reads the same way
	// twice.
	for _, name := range sortedKeys(args.Generated) {
		secret, err := c.secret(ctx, cfg, opts, name)
		if err != nil {
			return nil, err
		}
		version, err := secretsmanager.NewSecretVersion(ctx, name+"-version", &secretsmanager.SecretVersionArgs{
			SecretId:     secret.ID(),
			SecretString: pulumi.ToSecret(args.Generated[name]).(pulumi.StringOutput),
		}, child(c, opts, pulumi.AdditionalSecretOutputs([]string{"secretString"}))...)
		if err != nil {
			return nil, err
		}
		c.Versions = append(c.Versions, version)
		c.GeneratedARNs = append(c.GeneratedARNs, secret.Arn)
		// The version's own id, so a new value is a new task definition rather than a silent
		// change.
		c.Refs[name] = SecretRef{ARN: secret.Arn, VersionID: version.VersionId}
	}

	for _, name := range ManualSecrets {
		secret, err := c.secret(ctx, cfg, opts, name)
		if err != nil {
			return nil, err
		}
		if version := args.ManualVersions[name]; version != "" {
			c.Refs[name] = SecretRef{ARN: secret.Arn, VersionID: pulumi.String(version)}
		}
	}

	return c, ctx.RegisterResourceOutputs(c, pulumi.Map{})
}

func (c *Secrets) secret(
	ctx *pulumi.Context, cfg stackconfig.StackConfig, opts []pulumi.ResourceOption, name string,
) (*secretsmanager.Secret, error) {
	window := 0
	if cfg.IsProduction() {
		window = RecoveryWindowDays
	}
	secret, err := secretsmanager.NewSecret(ctx, name, &secretsmanager.SecretArgs{
		Name:                 pulumi.String(cfg.Name(cfg.Slug, name)),
		RecoveryWindowInDays: pulumi.Int(window),
	}, child(c, opts)...)
	if err != nil {
		return nil, err
	}
	c.ARNs[name] = secret.Arn
	return secret, nil
}
