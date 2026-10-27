package components

import (
	"fmt"

	"github.com/pulumi/pulumi-gcp/sdk/v10/go/gcp/secretmanager"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"{{ cookiecutter.go_module }}/infra/pulumi/internal/stackconfig"
)

// SecretNames is every Secret Manager container the stack creates, each of them empty.
{%- if cookiecutter.use_postgres == "yes" or cookiecutter.use_sentry == "yes" %}
var SecretNames = []string{
{%- if cookiecutter.use_postgres == "yes" %}
	// The `postgres` login's password, which only the migration job reads. `just infra-secrets`.
	"db-admin-password",
	// The HS256 key the service signs access tokens with. `just infra-secrets`.
	"jwt-secret",
{%- endif %}
{%- if cookiecutter.use_sentry == "yes" %}
	// Sentry's project DSN, which originates in Sentry. `gcloud secrets versions add` by hand.
	"sentry-dsn",
{%- endif %}
}
{%- else %}
var SecretNames = []string{}
{%- endif %}

// SecretRef is what a service needs to mount a secret: the container and the exact version to read.
type SecretRef struct {
	Secret  pulumi.StringInput
	Version pulumi.StringInput
}

// SecretsArgs is what Secrets needs beyond the configuration.
type SecretsArgs struct {
	// Readers maps every name in SecretNames to the one account granted secretAccessor on it. One
	// reader per secret: the migration job's account reads the database admin password and nothing
	// else, the service's account reads everything but it.
	Readers map[string]pulumi.StringInput

	// Versions maps a name in SecretNames to the version number its value was added as. An empty
	// or absent entry means "not populated yet", and the secret gets no reference.
	Versions map[string]string
}

// Secrets is the stack's Secret Manager containers.
//
// The house rule is that secret VALUES never live in infrastructure code, in a stack configuration
// file, or in state — ask Athena for the Pulumi standards (MUST-3), and see ADR-0023 for how this
// stack meets it. So this component creates containers and access grants and never a version: a
// value is added out of band — by `just infra-secrets` for the ones this repository can generate,
// by a human for the ones that originate somewhere else — and its version NUMBER, which is not a
// secret, goes into stack configuration. Until then a secret has no reference, so nothing can
// mount it, and a mounted empty container is an error at plan time rather than a revision that
// cannot start.
//
// Every reference pins a version number, never "latest". Cloud Run reads an environment secret
// when an instance STARTS, so with "latest" a rotation leaves the instances of one revision
// holding different values. Pinned, a rotation is a new revision, and a rollback restores the
// credential with the image. Ask Athena for the Cloud Run service standards.
type Secrets struct {
	pulumi.ResourceState

	// IDs maps a logical name to its Secret Manager secret id.
	IDs map[string]pulumi.StringOutput

	// Refs maps a logical name to a pinned reference, for every secret whose version number is in
	// stack configuration.
	Refs map[string]SecretRef
}

// NewSecrets creates every container and its one access grant.
func NewSecrets(
	ctx *pulumi.Context, cfg stackconfig.StackConfig, args SecretsArgs, opts ...pulumi.ResourceOption,
) (*Secrets, error) {
	// Fail here rather than mid-apply: a container nobody may read is a secret that silently
	// cannot be mounted.
	for _, name := range SecretNames {
		if args.Readers[name] == nil {
			return nil, fmt.Errorf("secret %q has no reader", name)
		}
	}

	c := &Secrets{
		IDs:  map[string]pulumi.StringOutput{},
		Refs: map[string]SecretRef{},
	}
	if err := ctx.RegisterComponentResource("service:infra:Secrets", cfg.Name("secrets"), c, opts...); err != nil {
		return nil, err
	}

	for _, name := range SecretNames {
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
		// Access is granted per secret, not project-wide, so a secret added later is not
		// automatically readable by everything that can already read this one.
		if _, err := secretmanager.NewSecretIamMember(ctx, name+"-accessor", &secretmanager.SecretIamMemberArgs{
			Project:  pulumi.String(cfg.Project),
			SecretId: secret.SecretId,
			Role:     pulumi.String("roles/secretmanager.secretAccessor"),
			Member:   pulumi.Sprintf("serviceAccount:%s", args.Readers[name]),
		}, child(c, opts)...); err != nil {
			return nil, err
		}
		c.IDs[name] = secret.SecretId
		if version := args.Versions[name]; version != "" {
			c.Refs[name] = SecretRef{Secret: secret.SecretId, Version: pulumi.String(version)}
		}
	}

	return c, ctx.RegisterResourceOutputs(c, pulumi.Map{})
}
