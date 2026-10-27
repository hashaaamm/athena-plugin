package components

import (
	"github.com/pulumi/pulumi-gcp/sdk/v10/go/gcp/secretmanager"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"{{ cookiecutter.go_module }}/infra/pulumi/internal/stackconfig"
)

// ManualSecrets are containers created empty, for values that originate outside this stack. A
// human populates them; nothing mounts them until they have a version.
{%- if cookiecutter.use_sentry == "yes" %}
var ManualSecrets = []string{"sentry-dsn"}
{%- else %}
var ManualSecrets = []string{}
{%- endif %}

// SecretRef is what a service needs to mount a secret: the container and the exact version to read.
type SecretRef struct {
	Secret  pulumi.StringInput
	Version pulumi.StringInput
}

// SecretsArgs is what Secrets needs beyond the configuration.
type SecretsArgs struct {
	// AccessorEmail is the one account granted secretAccessor on every container here.
	AccessorEmail pulumi.StringInput

	// Generated maps a logical name to a value this stack mints and no human ever reads. Each gets
	// its container and its first version.
	Generated map[string]pulumi.StringInput

	// ManualVersions maps a name in ManualSecrets to the version a human added. An empty or absent
	// entry means "not populated yet", and the secret gets no reference.
	ManualVersions map[string]string
}

// Secrets is the stack's Secret Manager containers.
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
//     number.
//   - Manual. Anything that genuinely originates outside — a Sentry DSN, a third-party API key.
//     The stack creates the empty container and grants access to it; a human adds the value with
//     `gcloud secrets versions add` and puts the version number it prints into stack
//     configuration. Until then the secret has no reference, so nothing can mount it.
//
// Every reference pins a version number, never "latest". Cloud Run reads an environment secret
// when an instance STARTS, so with "latest" a rotation leaves the instances of one revision
// holding different values. Pinned, a rotation is a new revision, and a rollback restores the
// credential with the image. Ask Athena for the Cloud Run service standards.
type Secrets struct {
	pulumi.ResourceState

	// IDs maps a logical name to its Secret Manager secret id.
	IDs map[string]pulumi.StringOutput

	// Versions is every version this component creates. Anything that mounts a secret must depend
	// on these, not merely on the containers — the container is not the dependency, the value is.
	// A service that waits only for the container races the version into existence and fails with
	// "version was not found".
	Versions []pulumi.Resource

	// Refs maps a logical name to a pinned reference, for every secret that has a version to pin.
	// A manual secret appears here only once its version number is in stack configuration, so
	// mounting an empty container is an error at plan time rather than a revision that cannot
	// start.
	Refs map[string]SecretRef
}

// NewSecrets creates every container, the generated versions, and the per-secret access grants.
func NewSecrets(
	ctx *pulumi.Context, cfg stackconfig.StackConfig, args SecretsArgs, opts ...pulumi.ResourceOption,
) (*Secrets, error) {
	c := &Secrets{
		IDs:  map[string]pulumi.StringOutput{},
		Refs: map[string]SecretRef{},
	}
	if err := ctx.RegisterComponentResource("service:infra:Secrets", cfg.Name("secrets"), c, opts...); err != nil {
		return nil, err
	}

	member := pulumi.Sprintf("serviceAccount:%s", args.AccessorEmail)

	// Sorted, so two runs register resources in the same order and a preview reads the same way
	// twice.
	for _, name := range sortedKeys(args.Generated) {
		secret, err := c.container(ctx, cfg, opts, name, member)
		if err != nil {
			return nil, err
		}
		version, err := secretmanager.NewSecretVersion(ctx, name+"-version", &secretmanager.SecretVersionArgs{
			Secret:     secret.ID(),
			SecretData: pulumi.ToSecret(args.Generated[name]).(pulumi.StringOutput),
		}, child(c, opts, pulumi.AdditionalSecretOutputs([]string{"secretData"}))...)
		if err != nil {
			return nil, err
		}
		c.Versions = append(c.Versions, version)
		// The version's own number, so a new value is a new revision rather than a silent change.
		c.Refs[name] = SecretRef{Secret: secret.SecretId, Version: version.Version}
	}

	for _, name := range ManualSecrets {
		secret, err := c.container(ctx, cfg, opts, name, member)
		if err != nil {
			return nil, err
		}
		if version := args.ManualVersions[name]; version != "" {
			c.Refs[name] = SecretRef{Secret: secret.SecretId, Version: pulumi.String(version)}
		}
	}

	return c, ctx.RegisterResourceOutputs(c, pulumi.Map{})
}

func (c *Secrets) container(
	ctx *pulumi.Context, cfg stackconfig.StackConfig, opts []pulumi.ResourceOption, name string, member pulumi.StringInput,
) (*secretmanager.Secret, error) {
	secret, err := secretmanager.NewSecret(ctx, name, &secretmanager.SecretArgs{
		Project:  pulumi.String(cfg.Project),
		SecretId: pulumi.String(cfg.Name(cfg.Slug, name)),
		Replication: &secretmanager.SecretReplicationArgs{
			Auto: &secretmanager.SecretReplicationAutoArgs{},
		},
	}, child(c, opts)...)
	if err != nil {
		return nil, err
	}
	// Access is granted per secret, not project-wide, so a secret added later is not automatically
	// readable by everything that can already read this one.
	if _, err := secretmanager.NewSecretIamMember(ctx, name+"-accessor", &secretmanager.SecretIamMemberArgs{
		Project:  pulumi.String(cfg.Project),
		SecretId: secret.SecretId,
		Role:     pulumi.String("roles/secretmanager.secretAccessor"),
		Member:   member,
	}, child(c, opts)...); err != nil {
		return nil, err
	}
	c.IDs[name] = secret.SecretId
	return secret, nil
}
