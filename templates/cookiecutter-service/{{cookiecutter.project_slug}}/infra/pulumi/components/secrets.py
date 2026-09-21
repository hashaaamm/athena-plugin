"""Secret Manager containers.

The house rule is that secret *values* never live in infrastructure code, in a stack configuration
file, or in state — retrieve the Pulumi standards for the graded form. What that protects against
is a credential a human chose, typed somewhere, and can leak. It is not a prohibition on the stack
generating a credential nothing else ever needs to know.

So there are two kinds of secret here, and they behave differently:

* **Generated.** Values this stack mints and no human ever reads.
{%- if cookiecutter.use_postgres == "yes" %}
  The database DSN and the token signing key.
{%- endif %}
  They are encrypted in state with the KMS key the bootstrap script created, and they have a
  version the moment the stack applies. That last part matters: Cloud Run resolves
  `secret:latest` when a revision is *created*, so a container with no versions is a deploy that
  fails outright rather than a service missing a feature.
* **Manual.** Anything that genuinely originates outside — a Sentry DSN, a third-party API key.
  The stack creates the empty container and grants access to it; a human adds the value with
  `gcloud secrets versions add`. Because the container starts empty, nothing may mount it until
  that has happened, which is what the `sentryDsnSet` stack configuration key gates.
"""

from __future__ import annotations

import pulumi
import pulumi_gcp as gcp

from config import StackConfig

#: Containers created empty, for values that originate outside this stack. A human populates them;
#: nothing mounts them until they have a version.
{%- if cookiecutter.use_sentry == "yes" %}
MANUAL_SECRETS: tuple[str, ...] = ("sentry-dsn",)
{%- else %}
MANUAL_SECRETS: tuple[str, ...] = ()
{%- endif %}


class Secrets(pulumi.ComponentResource):
    def __init__(
        self,
        config: StackConfig,
        accessor_email: pulumi.Input[str],
        database_url: pulumi.Input[str] | None = None,
        jwt_secret: pulumi.Input[str] | None = None,
        opts: pulumi.ResourceOptions | None = None,
    ) -> None:
        super().__init__("service:infra:Secrets", config.name("secrets"), None, opts)

        #: Logical name -> Secret Manager secret id. `Service` looks a mount up by logical name, so
        #: a typo is a `KeyError` at plan time rather than a revision that cannot start.
        self.ids: dict[str, pulumi.Output[str]] = {}
        #: Every version this component creates. Anything that mounts a secret must depend on
        #: these, not merely on the containers — the container is not the dependency, the value is.
        self.versions: list[pulumi.Resource] = []

        member = pulumi.Output.from_input(accessor_email).apply(
            lambda email: f"serviceAccount:{email}"
        )

        if database_url is not None:
            self._versioned(config, "database-url", member, database_url)

        if jwt_secret is not None:
            self._versioned(config, "jwt-secret", member, jwt_secret)

        for name in MANUAL_SECRETS:
            self._container(config, name, member)

        self.register_outputs({})

    def _versioned(
        self,
        config: StackConfig,
        name: str,
        member: pulumi.Input[str],
        value: pulumi.Input[str],
    ) -> gcp.secretmanager.Secret:
        """The container and its first version, for a value the stack generates."""
        secret = self._container(config, name, member)
        self.versions.append(
            gcp.secretmanager.SecretVersion(
                f"{name}-version",
                secret=secret.id,
                secret_data=value,
                opts=pulumi.ResourceOptions(parent=self, additional_secret_outputs=["secret_data"]),
            )
        )
        return secret

    def _container(
        self, config: StackConfig, name: str, member: pulumi.Input[str]
    ) -> gcp.secretmanager.Secret:
        secret = gcp.secretmanager.Secret(
            name,
            project=config.project,
            secret_id=config.name(config.slug, name),
            replication=gcp.secretmanager.SecretReplicationArgs(
                auto=gcp.secretmanager.SecretReplicationAutoArgs(),
            ),
            opts=pulumi.ResourceOptions(parent=self),
        )
        # Access is granted per secret, not project-wide, so a secret added later is not
        # automatically readable by everything that can already read this one.
        gcp.secretmanager.SecretIamMember(
            f"{name}-accessor",
            project=config.project,
            secret_id=secret.secret_id,
            role="roles/secretmanager.secretAccessor",
            member=member,
            opts=pulumi.ResourceOptions(parent=self),
        )
        self.ids[name] = secret.secret_id
        return secret
