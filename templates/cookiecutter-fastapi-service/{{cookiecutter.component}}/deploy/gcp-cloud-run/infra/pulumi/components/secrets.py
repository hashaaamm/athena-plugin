"""Secret Manager containers.

The house rule is that secret *values* never live in infrastructure code, in a stack configuration
file, or in state — Pulumi standards MUST-3, and ADR-0023 for how this template meets it. So this
component creates containers and nothing else: every one is empty when the stack applies, and grants
read access to exactly one account.

Every value is added out of band and its version number recorded in stack configuration:
{%- if cookiecutter.use_postgres == "yes" %}

* `jwt-secret` and `db-admin-password` by `just infra-secrets`, which generates each value, adds it
  as a version without printing it, and runs `pulumi config set` with the number it gets back.
{%- endif %}
{%- if cookiecutter.use_sentry == "yes" %}

* `sentry-dsn` by a human, because the DSN comes from a Sentry account: `gcloud secrets versions
  add` with the value on stdin, then the number it prints into `sentryDsnVersion`.
{%- endif %}

A secret has a reference only once its version is configured, so nothing can mount an empty
container: mounting one is a `KeyError` at plan time rather than a revision that cannot start.

Every reference pins a version number, never `latest`. Cloud Run reads an environment secret when
an instance *starts*, so with `latest` a rotation leaves the instances of one revision holding
different values. Pinned, a rotation is a new revision, and a rollback restores the credential with
the image. Retrieve the Cloud Run standards for the graded form.
"""

from __future__ import annotations

from collections.abc import Mapping
from dataclasses import dataclass

import pulumi
import pulumi_gcp as gcp

from config import StackConfig


@dataclass(frozen=True, slots=True)
class SecretRef:
    """What a service needs to mount a secret: the container and the exact version to read."""

    secret: pulumi.Input[str]
    version: pulumi.Input[str]


class Secrets(pulumi.ComponentResource):
    def __init__(
        self,
        config: StackConfig,
        accessors: Mapping[str, pulumi.Input[str]],
        versions: Mapping[str, str] | None = None,
        opts: pulumi.ResourceOptions | None = None,
    ) -> None:
        """`accessors` is every secret this stack has, by logical name, mapped to the one service
        account that may read it. `versions` is the configured version number of each, where there
        is one."""
        super().__init__("service:infra:Secrets", config.name("secrets"), None, opts)

        #: Logical name -> Secret Manager secret id.
        self.ids: dict[str, pulumi.Output[str]] = {}
        #: Every grant this component creates. A revision that starts before its account can read
        #: its secrets fails to start, so anything that mounts one depends on these.
        self.grants: list[pulumi.Resource] = []
        #: Logical name -> a pinned reference, for every secret whose version is configured.
        self.refs: dict[str, SecretRef] = {}

        versions = versions or {}
        for name, accessor in accessors.items():
            secret = self._container(config, name, accessor)
            if versions.get(name):
                self.refs[name] = SecretRef(secret=secret.secret_id, version=versions[name])

        self.register_outputs({})

    def _container(
        self, config: StackConfig, name: str, accessor_email: pulumi.Input[str]
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
        # Access is granted per secret and per account, not project-wide, so a secret added later
        # is not automatically readable by everything that can already read this one, and the
        # service cannot read the migration's password.
        self.grants.append(
            gcp.secretmanager.SecretIamMember(
                f"{name}-accessor",
                project=config.project,
                secret_id=secret.secret_id,
                role="roles/secretmanager.secretAccessor",
                member=pulumi.Output.from_input(accessor_email).apply(
                    lambda email: f"serviceAccount:{email}"
                ),
                opts=pulumi.ResourceOptions(parent=self),
            )
        )
        self.ids[name] = secret.secret_id
        return secret
