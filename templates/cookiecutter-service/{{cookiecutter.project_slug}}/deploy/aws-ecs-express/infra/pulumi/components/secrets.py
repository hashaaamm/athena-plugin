"""Secrets Manager secrets.

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
  version the moment the stack applies, which the service pins by its `VersionId`.
* **Manual.** Anything that genuinely originates outside — a Sentry DSN, a third-party API key.
  The stack creates the empty secret; a human adds the value with
  `aws secretsmanager put-secret-value` and puts the `VersionId` it prints into stack
  configuration. Until then the secret has no reference, so nothing can mount it.

**Every reference pins a version** — ADR-0016's rule, applied to ECS. An ECS secret is read when a
task *starts*, so a reference to the current value (`AWSCURRENT`, which is what a bare ARN means)
lets the tasks of one deployment hold different values after a rotation: the ones started before it
keep the old credential, the ones started after get the new one, and revoking the old one at its
issuer is an intermittent outage. Pinned, a new value is a new deployment, and a rollback restores
the credential with the image. The reference is `<secret ARN>:::<VersionId>` — the json-key and the
version-stage fields left empty, the version id in the last one.
"""

from __future__ import annotations

from collections.abc import Mapping
from dataclasses import dataclass

import pulumi
import pulumi_aws as aws

from config import StackConfig

#: Secrets created empty, for values that originate outside this stack. A human populates them;
#: nothing mounts them until their version id is in stack configuration.
{%- if cookiecutter.use_sentry == "yes" %}
MANUAL_SECRETS: tuple[str, ...] = ("sentry-dsn",)
{%- else %}
MANUAL_SECRETS: tuple[str, ...] = ()
{%- endif %}

#: Days a deleted secret waits before it is really gone, in the production stack. Everywhere else it
#: is deleted at once: a secret pending deletion still holds its name, so a destroy and re-create
#: of a `dev` stack would otherwise fail on a name that is "already scheduled for deletion".
PRODUCTION_RECOVERY_DAYS = 30


@dataclass(frozen=True, slots=True)
class SecretRef:
    """What a container needs to mount a secret: the secret and the exact version to read."""

    arn: pulumi.Input[str]
    version_id: pulumi.Input[str]

    def value_from(self) -> pulumi.Output[str]:
        """The ECS `valueFrom`: `<arn>:<json-key>:<version-stage>:<version-id>`, with the two middle
        fields empty. Never the bare ARN, which means `AWSCURRENT`."""
        return pulumi.Output.concat(self.arn, ":::", self.version_id)


class Secrets(pulumi.ComponentResource):
    def __init__(
        self,
        config: StackConfig,
        database_url: pulumi.Input[str] | None = None,
        jwt_secret: pulumi.Input[str] | None = None,
        manual_versions: Mapping[str, str] | None = None,
        opts: pulumi.ResourceOptions | None = None,
    ) -> None:
        super().__init__("service:infra:Secrets", config.name("secrets"), None, opts)

        #: Logical name -> secret ARN, for every secret this stack created. The execution role may
        #: read exactly these, and nothing else in the account.
        self.arns: dict[str, pulumi.Output[str]] = {}
        #: The subset whose value the stack generated. The pull-request preview reads these when it
        #: refreshes their versions — see `components/identities.py`.
        self.generated_arns: dict[str, pulumi.Output[str]] = {}
        #: Every version this component creates. Anything that mounts a secret must depend on
        #: these, not merely on the secrets — the secret is not the dependency, the value is.
        self.versions: list[pulumi.Resource] = []
        #: Logical name -> a pinned reference, for every secret that has a version to pin. A manual
        #: secret appears here only once its version id is in stack configuration, so mounting an
        #: empty secret is a `KeyError` at plan time rather than a task that cannot start.
        self.refs: dict[str, SecretRef] = {}

        if database_url is not None:
            self._versioned(config, "database-url", database_url)

        if jwt_secret is not None:
            self._versioned(config, "jwt-secret", jwt_secret)

        manual_versions = manual_versions or {}
        for name in MANUAL_SECRETS:
            secret = self._secret(config, name)
            if manual_versions.get(name):
                self.refs[name] = SecretRef(arn=secret.arn, version_id=manual_versions[name])

        self.register_outputs({})

    def _versioned(
        self, config: StackConfig, name: str, value: pulumi.Input[str]
    ) -> aws.secretsmanager.Secret:
        """The secret and its first version, for a value the stack generates."""
        secret = self._secret(config, name)
        version = aws.secretsmanager.SecretVersion(
            f"{name}-version",
            secret_id=secret.id,
            secret_string=value,
            opts=pulumi.ResourceOptions(parent=self, additional_secret_outputs=["secret_string"]),
        )
        self.versions.append(version)
        self.generated_arns[name] = secret.arn
        # The version's own id, so a new value is a new deployment rather than a silent change.
        self.refs[name] = SecretRef(arn=secret.arn, version_id=version.version_id)
        return secret

    def _secret(self, config: StackConfig, name: str) -> aws.secretsmanager.Secret:
        # The account's default `aws/secretsmanager` key. A customer-managed key would need a key
        # policy of its own granting the execution role, which buys nothing the per-secret IAM grant
        # does not.
        secret = aws.secretsmanager.Secret(
            name,
            name=config.name(config.slug, name),
            description=f"{config.slug}: {name}",
            recovery_window_in_days=PRODUCTION_RECOVERY_DAYS if config.is_production else 0,
            opts=pulumi.ResourceOptions(parent=self),
        )
        self.arns[name] = secret.arn
        return secret
