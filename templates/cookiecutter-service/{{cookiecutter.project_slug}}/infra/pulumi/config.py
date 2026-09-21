"""Typed stack configuration.

Every value the stack reads is resolved here, once. Components take a frozen `StackConfig` rather
than calling `pulumi.Config()` themselves, which is what keeps a component constructible in a test
and stops configuration keys being invented at three different call sites.
"""

from __future__ import annotations

import hashlib
from dataclasses import dataclass

import pulumi

#: GCP service-account ids are 6-30 characters. The environment suffix and the role suffix are
#: fixed-width, so the project slug is what has to give when a name is long.
MAX_ACCOUNT_ID = 30

#: Workload Identity Pool ids are 4-32 characters, and the same squeeze applies.
MAX_POOL_ID = 32

#: Characters of digest appended to a pool id. Four is 65,536 values, which is not a cryptographic
#: guarantee and does not need to be: the thing it separates is two services a human deliberately
#: named in one project, not an adversary hunting a collision.
DISAMBIGUATOR = 4


def _digest(*parts: str) -> str:
    """A stable short id for a name, derived from everything that makes it that name.

    Deterministic, and that is the whole requirement. A *random* suffix would be more unique and
    would also be unusable: Pulumi would see a different pool id on every run, replace the pool
    every time, and each replacement puts the old id into GCP's 30-day hold. A name that cannot be
    computed twice is not a name.

    It hashes the repository as well as the slug, so two teams who both call their service `orders`
    in one GCP project get different pools rather than a 409 and an argument about who was first.
    """
    return hashlib.sha256("\n".join(parts).encode()).hexdigest()[:DISAMBIGUATOR]


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

    def pool_id(self, kind: str) -> str:
        """A Workload Identity Pool id, unique within the *project* rather than the stack.

        `name()` is not enough here, and the difference between them is a 409 on somebody else's
        resource. A pool id is unique across a GCP project, and two services generated from this
        template into the same project both want one — so an id built from the kind and the
        environment alone is already taken the second time, by a pool this stack does not own and
        must not adopt. Every other project-global name in this stack already carries the slug for
        exactly this reason: the Cloud SQL instance, the registry repository, each secret and each
        service account. The pool is the one that did not.

        Deleting a pool to free its name does not help either: GCP keeps it for 30 days before the
        id can be reused, which turns a rename into a month of waiting. Get it right the first time.

        The trailing digest is what makes "unique" true rather than usually-true. 32 characters is
        not many, so a long slug gets trimmed, and trimming is exactly where two distinct services
        become one name: `platform-billing-service-api` and `platform-billing-service-web` both cut
        down to `platform-billing-serv`. The digest is taken over the *untrimmed* slug and the
        repository, so what the trim throws away is still represented in the result.
        """
        suffix = f"-{kind}-{self.environment}-{_digest(self.slug, self.github_repository)}"
        head = self.slug[: MAX_POOL_ID - len(suffix)].rstrip("-")
        pool = f"{head}{suffix}"
        if not 4 <= len(pool) <= MAX_POOL_ID:
            raise ValueError(f"workload identity pool id {pool!r} is not 4-32 characters")
        if pool.startswith("gcp-"):
            raise ValueError(f"workload identity pool id {pool!r} may not start with 'gcp-'")
        return pool

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
