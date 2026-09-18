"""Typed stack configuration.

Every value the stack reads is resolved here, once. Components take a frozen `StackConfig` rather
than calling `pulumi.Config()` themselves, which is what keeps a component constructible in a test
and stops configuration keys being invented at three different call sites.
"""

from __future__ import annotations

from dataclasses import dataclass

import pulumi

#: GCP service-account ids are 6-30 characters. The environment suffix and the role suffix are
#: fixed-width, so the project slug is what has to give when a name is long.
MAX_ACCOUNT_ID = 30


@dataclass(frozen=True, slots=True)
class StackConfig:
    project: str
    region: str
    environment: str
    #: `owner/repo`. The Workload Identity provider's attribute condition is built from this, and
    #: it is the single line that stops any repository on GitHub from assuming the CI identity.
    github_repository: str
    #: The base name every resource is derived from — the cookiecutter slug.
    slug: str

    min_instances: int = 0
    max_instances: int = 4
{%- if cookiecutter.use_postgres == "yes" %}

    database_tier: str = "db-f1-micro"
    database_disk_gb: int = 10
{%- endif %}
{%- if cookiecutter.use_sentry == "yes" %}

    #: Whether a version has been added to the Sentry DSN secret container.
    #:
    #: False by default, and the default has to stay applyable. Cloud Run resolves `secret:latest`
    #: when a revision is *created*, so mounting a container with no versions is a deploy that
    #: fails outright rather than a service that starts without error tracking. The stack creates
    #: the empty container; a human runs `gcloud secrets versions add`; only then does this flip to
    #: true and the mount appear.
    sentry_dsn_set: bool = False
{%- endif %}

    @property
    def is_production(self) -> bool:
        return self.environment == "prod"

    def name(self, *parts: str) -> str:
        """Resource names carry the environment, so two stacks can share a project safely."""
        return "-".join((*parts, self.environment))

    def account_id(self, role: str) -> str:
        """A service-account id for `role`, trimmed to what GCP accepts.

        The slug is the part that gets shortened, because the role and the environment are what
        make two accounts different from each other. Shortening silently would be wrong if it could
        collide, and it cannot: the role suffix is unique per account within a stack.
        """
        suffix = f"-{role}-{self.environment}"
        head = self.slug[: MAX_ACCOUNT_ID - len(suffix)].rstrip("-")
        account = f"{head}{suffix}"
        if not 6 <= len(account) <= MAX_ACCOUNT_ID:
            raise ValueError(f"service account id {account!r} is not 6-30 characters")
        return account

    @property
    def registry_host(self) -> str:
        return f"{self.region}-docker.pkg.dev"


def load() -> StackConfig:
    config = pulumi.Config()
    return StackConfig(
        project=config.require("project"),
        region=config.require("region"),
        environment=config.require("environment"),
        github_repository=config.require("githubRepository"),
        slug=config.require("slug"),
        min_instances=config.get_int("minInstances") or 0,
        max_instances=config.get_int("maxInstances") or 4,
{%- if cookiecutter.use_postgres == "yes" %}
        database_tier=config.get("databaseTier") or "db-f1-micro",
        database_disk_gb=config.get_int("databaseDiskGb") or 10,
{%- endif %}
{%- if cookiecutter.use_sentry == "yes" %}
        sentry_dsn_set=config.get_bool("sentryDsnSet") or False,
{%- endif %}
    )
