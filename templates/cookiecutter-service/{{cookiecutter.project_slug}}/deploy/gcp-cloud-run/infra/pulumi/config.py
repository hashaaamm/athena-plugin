"""Typed stack configuration.

Every value the stack reads is resolved here, once. Components take a frozen `StackConfig` rather
than calling `pulumi.Config()` themselves, which is what keeps a component constructible in a test
and stops configuration keys being invented at three different call sites.
"""

from __future__ import annotations

import hashlib
from dataclasses import dataclass
from urllib.parse import urlsplit

import pulumi

#: GCP service-account ids are 6-30 characters. The environment suffix and the role suffix are
#: fixed-width, so the project slug is what has to give when a name is long.
MAX_ACCOUNT_ID = 30

#: Workload Identity Pool ids are 4-32 characters, and the same squeeze applies.
MAX_POOL_ID = 32

#: What `ENVIRONMENT` may be set to on the deployed service. It is `app/core/config.py`'s
#: `Environment` literal, and nothing else parses: a typo here is a container that exits on its
#: first settings load, several minutes into an apply.
APP_ENVIRONMENTS = ("local", "test", "staging", "production")

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
{%- if cookiecutter.use_postgres == "yes" %}


#: Where the refresh token travels: `REFRESH_TRANSPORT` in `app/core/config.py`, which accepts these
#: two and nothing else.
REFRESH_TRANSPORTS = ("body", "cookie")
{%- endif %}


def parse_origins(raw: str) -> tuple[str, ...]:
    """`corsOrigins` as a list: comma-separated, trimmed, empty entries dropped."""
    return tuple(origin.strip() for origin in raw.split(",") if origin.strip())


def _is_origin(origin: str) -> bool:
    """Exactly what a browser sends in an `Origin` header: a scheme, a host, an optional port.

    The application applies the same rule to `CORS_ORIGINS` and refuses to start on a miss, so a
    miss here is caught at preview rather than as a revision that never becomes ready. A trailing
    slash or a path never matches a real `Origin`, so the entry would be dead configuration that
    looks alive; `*` is refused because a wildcard origin cannot carry credentials anyway.
    """
    try:
        parts = urlsplit(origin)
        _ = parts.port  # raises on a port that is not one
    except ValueError:
        return False
    return (
        parts.scheme in ("http", "https")
        and bool(parts.hostname)
        and not (parts.path or parts.query or parts.fragment)
        and "@" not in parts.netloc
        and "*" not in origin
    )


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

    #: What the *application* is told it is running as, which is a different question from what
    #: this stack is called. `environment` above names resources — it is why the Cloud Run service
    #: is `api-dev` — and the application has never heard of it. This one is the `ENVIRONMENT`
    #: variable on the revision, and it decides how the service behaves: whether `/docs` is
    #: served, whether a placeholder database password is refused, how logs are formatted.
    #:
    #: They are set separately because they vary separately. A `dev` stack that anyone outside the
    #: team can reach is `production` to the application; a `prod` stack standing in for a
    #: customer demo is not. Defaulting to `staging` is the honest reading of a first deploy:
    #: production-shaped logging and a placeholder password still refused, with `/docs` reachable,
    #: because the first thing anybody does with a new URL is open it and look for the API.
    #: Set it to `production` in the stack that real users reach. See infra/README.md.
    app_environment: str = "staging"

    min_instances: int = 0
    max_instances: int = 4

    #: The origins a browser client is served from — `CORS_ORIGINS` on the service. Empty means no
    #: browser on another origin may call this API, which is right until there is one. A browser
    #: client is its own repository and stack, generated from `cookiecutter-react-app` (ADR-0022);
    #: its `site_url` export is the value that goes here.
    cors_origins: tuple[str, ...] = ()
{%- if cookiecutter.use_postgres == "yes" %}

    #: Where the refresh token travels — `REFRESH_TRANSPORT` on the service. `body` for a
    #: server-to-server caller; `cookie` for a browser, which must keep it where script cannot read
    #: it. The cookie routes' CSRF check accepts only listed origins, so `cookie` needs
    #: `cors_origins`.
    refresh_transport: str = "body"

    database_tier: str = "db-f1-micro"
    database_disk_gb: int = 10
{%- endif %}
{%- if cookiecutter.use_sentry == "yes" %}

    #: The Sentry DSN secret version the service pins, e.g. `"1"`. Empty means "not mounted".
    #:
    #: Empty by default, and the default has to stay applyable: mounting a container with no
    #: versions is a deploy that fails outright rather than a service that starts without error
    #: tracking. The stack creates the empty container; a human runs `gcloud secrets versions add`
    #: and puts the number it prints here; only then does the mount appear. Rotating the DSN is the
    #: same two steps with the next number, which deploys a new revision.
    sentry_dsn_version: str = ""
{%- endif %}

    def __post_init__(self) -> None:
        problems: list[str] = []
        if self.app_environment not in APP_ENVIRONMENTS:
            problems.append(
                f"appEnvironment {self.app_environment!r} is not one of {APP_ENVIRONMENTS}"
            )
        problems.extend(
            f"corsOrigins entry {origin!r} must be an origin such as https://app.example.com"
            for origin in self.cors_origins
            if not _is_origin(origin)
        )
{%- if cookiecutter.use_postgres == "yes" %}
        if self.refresh_transport not in REFRESH_TRANSPORTS:
            problems.append(
                f"refreshTransport {self.refresh_transport!r} is not one of {REFRESH_TRANSPORTS}"
            )
        elif self.refresh_transport == "cookie" and not self.cors_origins:
            # The service would refuse to start with this pair, several minutes into an apply.
            problems.append(
                "refreshTransport cookie requires corsOrigins: the CSRF check only accepts "
                "listed origins"
            )
{%- endif %}
        if problems:
            raise ValueError("; ".join(problems))

    @property
    def browser_env(self) -> dict[str, str]:
        """The service's environment for the browser that calls it, and for nothing else.

        `CORS_ORIGINS` only when there is an origin to allow: unset and empty mean the same to the
        application, and unset is the one a reader does not have to wonder about.
        """
        env: dict[str, str] = {}
        if self.cors_origins:
            env["CORS_ORIGINS"] = ",".join(self.cors_origins)
{%- if cookiecutter.use_postgres == "yes" %}
        env["REFRESH_TRANSPORT"] = self.refresh_transport
{%- endif %}
        return env

    @property
    def is_production(self) -> bool:
        """Whether this *stack* is the production one. Deletion protection and point-in-time
        recovery hang off it, and neither is a question about how the application behaves —
        `app_environment` is that question."""
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
        app_environment=config.get("appEnvironment") or "staging",
        min_instances=config.get_int("minInstances") or 0,
        max_instances=config.get_int("maxInstances") or 4,
        cors_origins=parse_origins(config.get("corsOrigins") or ""),
{%- if cookiecutter.use_postgres == "yes" %}
        refresh_transport=(config.get("refreshTransport") or "body").strip(),
        database_tier=config.get("databaseTier") or "db-f1-micro",
        database_disk_gb=config.get_int("databaseDiskGb") or 10,
{%- endif %}
{%- if cookiecutter.use_sentry == "yes" %}
        sentry_dsn_version=(config.get("sentryDsnVersion") or "").strip(),
{%- endif %}
    )
