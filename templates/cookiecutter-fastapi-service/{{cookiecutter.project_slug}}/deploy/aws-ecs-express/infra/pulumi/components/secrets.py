"""Secrets Manager secrets.

The house rule is that secret *values* never live in infrastructure code, in a stack configuration
file, or in state — Pulumi standards MUST-3, and ADR-0023 for how this template meets it. So this
component creates secrets and nothing else: every one is empty when the stack applies.

Every value is added out of band and its `VersionId` recorded in stack configuration:
{%- if cookiecutter.use_postgres == "yes" %}

* `jwt-secret` by `just infra-secrets`, which generates the key, adds it as a version without
  printing it, and runs `pulumi config set jwtSecretVersion` with the id it gets back.
{%- endif %}
{%- if cookiecutter.use_sentry == "yes" %}

* `sentry-dsn` by a human, because the DSN comes from a Sentry account: `aws secretsmanager
  put-secret-value` with the value on stdin, then the `VersionId` it prints into `sentryDsnVersion`.
{%- endif %}
{%- if cookiecutter.use_postgres == "yes" %}

The database's master password is not here at all: RDS generates, stores and rotates it in a secret
of its own (`components/database.py`).
{%- endif %}

A secret has a reference only once its version is configured, so nothing can mount an empty one:
mounting one is a `KeyError` at plan time rather than a task that cannot start.

**Every reference pins a version** — ADR-0016's rule, applied to ECS. An ECS secret is read when a
task *starts*, so a reference to the current value (`AWSCURRENT`, which is what a bare ARN means)
lets the tasks of one deployment hold different values after a rotation: the ones started before it
keep the old credential, the ones started after get the new one, and revoking the old one at its
issuer is an intermittent outage. Pinned, a new value is a new deployment, and a rollback restores
the credential with the image. The reference is `<secret ARN>:::<VersionId>` — the json-key and the
version-stage fields left empty, the version id in the last one.
"""

from __future__ import annotations

from collections.abc import Mapping, Sequence
from dataclasses import dataclass

import pulumi
import pulumi_aws as aws

from config import StackConfig

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
        names: Sequence[str] = (),
        versions: Mapping[str, str] | None = None,
        opts: pulumi.ResourceOptions | None = None,
    ) -> None:
        """`names` is every secret this stack has, by logical name; `versions` the configured
        `VersionId` of each, where there is one."""
        super().__init__("service:infra:Secrets", config.name("secrets"), None, opts)

        #: Logical name -> secret ARN, for every secret this stack created. The service's execution
        #: role may read exactly these, and nothing else in the account.
        self.arns: dict[str, pulumi.Output[str]] = {}
        #: Logical name -> a pinned reference, for every secret whose version is configured.
        self.refs: dict[str, SecretRef] = {}

        versions = versions or {}
        for name in names:
            secret = self._secret(config, name)
            if versions.get(name):
                self.refs[name] = SecretRef(arn=secret.arn, version_id=versions[name])

        self.register_outputs({})

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
