"""Typed stack configuration.

Every value the stack reads is resolved here, once. Components take a frozen `StackConfig` rather
than calling `pulumi.Config()` themselves, which is what keeps a component constructible in a test
and stops configuration keys being invented at three different call sites.

Everything that can be wrong is refused here, at load, rather than by AWS several minutes into an
apply with half the stack created: an account id that is not one, a CPU and memory pair Fargate
does not offer, a task range Express Mode cannot run.
"""

from __future__ import annotations

import hashlib
import re
from dataclasses import dataclass
from urllib.parse import urlsplit

import pulumi

#: IAM role names are at most 64 characters. The role suffix and the environment are what make two
#: roles different from each other, so the project slug is what has to give when a name is long.
MAX_ROLE_NAME = 64

#: RDS instance identifiers are 1-63 characters, start with a letter, and hold letters, digits and
#: single hyphens.
MAX_DB_IDENTIFIER = 63

#: What `ENVIRONMENT` may be set to on the deployed service. It is `app/core/config.py`'s
#: `Environment` literal, and nothing else parses: a typo here is a container that exits on its
#: first settings load, several minutes into an apply.
APP_ENVIRONMENTS = ("local", "test", "staging", "production")

#: Fargate's CPU units -> the memory sizes (MiB) it offers at that CPU. A pair outside this table is
#: refused by ECS at task-definition time, which is mid-apply. Capped at 8192 MiB, the most an
#: Express Mode service accepts.
FARGATE_MEMORY: dict[str, tuple[int, ...]] = {
    "256": (512, 1024, 2048),
    "512": tuple(range(1024, 4097, 1024)),
    "1024": tuple(range(2048, 8193, 1024)),
    "2048": tuple(range(4096, 8193, 1024)),
    "4096": tuple(range(8192, 8193, 1024)),
}

#: Express Mode puts the load balancer and the tasks in the same subnets and needs at least two;
#: three is every AZ most regions have to give.
AVAILABILITY_ZONE_COUNTS = (2, 3)

#: gp3's floor. RDS refuses a smaller allocation for this storage type.
MIN_DATABASE_STORAGE_GB = 20

#: Characters of digest appended to a name that had to be trimmed. Four is 65,536 values, which is
#: not a cryptographic guarantee and does not need to be: the thing it separates is two services a
#: human deliberately named in one account, not an adversary hunting a collision.
DISAMBIGUATOR = 4


def _digest(*parts: str) -> str:
    """A stable short id for a name, derived from everything that makes it that name.

    Deterministic, and that is the whole requirement. A random suffix would be more unique and
    unusable: Pulumi would see a new name on every run and replace the resource every time.
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
    miss here is caught at preview rather than as a deployment whose tasks never become healthy. A
    trailing slash or a path never matches a real `Origin`, so the entry would be dead
    configuration that looks alive; `*` is refused because a wildcard origin cannot carry
    credentials anyway.
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
    #: The 12-digit AWS account this stack is allowed to touch. The provider refuses any other, so
    #: a terminal holding the wrong credentials fails before it plans anything.
    account_id: str
    region: str
    environment: str
    #: `owner/repo`. The GitHub OIDC trust policies are built from this, and it is the single line
    #: that stops any repository on GitHub from assuming the CI roles.
    github_repository: str
    #: The base name every resource is derived from — the cookiecutter slug.
    slug: str
    #: The Pulumi project name, from `Pulumi.yaml`. The pull-request preview's access to the shared
    #: state bucket is scoped to this project's directories in it, and nothing else.
    pulumi_project: str
    #: The Pulumi stack, for the `pulumi-stack` tag every resource carries.
    stack: str = "dev"

    #: What the *application* is told it is running as, which is a different question from what
    #: this stack is called. `environment` above names resources — it is why the Express service
    #: is `api-dev` — and the application has never heard of it. This one is the `ENVIRONMENT`
    #: variable on the task, and it decides how the service behaves: whether `/docs` is served,
    #: whether a placeholder database password is refused, how logs are formatted.
    #:
    #: They are set separately because they vary separately. A `dev` stack that anyone outside the
    #: team can reach is `production` to the application; a `prod` stack standing in for a
    #: customer demo is not. Defaulting to `staging` is the honest reading of a first deploy:
    #: production-shaped logging and a placeholder password still refused, with `/docs` reachable,
    #: because the first thing anybody does with a new URL is open it and look for the API.
    #: Set it to `production` in the stack that real users reach. See infra/README.md.
    app_environment: str = "staging"

    #: The start of the `sub` claim GitHub puts in this repository's OIDC tokens — everything
    #: before `:environment:…` or `:pull_request`. Empty means the name-only form,
    #: `repo:<owner>/<repo>`.
    #:
    #: GitHub now issues an immutable form for many repositories,
    #: `repo:<owner>@<owner-id>/<repo>@<repo-id>`, so that a renamed or re-registered account
    #: cannot inherit the trust. A trust policy written for the name-only form then matches no
    #: token at all, and CI fails with "Not authorized to perform sts:AssumeRoleWithWebIdentity".
    #: The repository reports its own prefix:
    #: `gh api repos/<owner>/<repo>/actions/oidc/customization/sub --jq .sub_claim_prefix`.
    #: infra/README.md has the step, and sync-github.sh refuses to un-gate CD while the two
    #: disagree.
    github_subject_prefix: str = ""

    #: Express Mode cannot scale to zero, so the floor is one task per service — and that task is a
    #: standing charge. infra/README.md has the cost table.
    min_tasks: int = 1
    max_tasks: int = 4
    #: Fargate CPU units and memory in MiB, as strings because that is what ECS takes.
    cpu: str = "256"
    memory: str = "512"
    availability_zone_count: int = 2

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

    database_instance_class: str = "db.t4g.micro"
    database_storage_gb: int = MIN_DATABASE_STORAGE_GB
    database_multi_az: bool = False
{%- endif %}
{%- if cookiecutter.use_sentry == "yes" %}

    #: The Secrets Manager `VersionId` of the Sentry DSN the service pins. Empty means "not
    #: mounted".
    #:
    #: Empty by default, and the default has to stay applyable: an ECS secret pointing at a secret
    #: with no value is a task that cannot start, rather than a service that starts without error
    #: tracking. The stack creates the empty secret; a human runs
    #: `aws secretsmanager put-secret-value` and puts the `VersionId` it prints here; only then does
    #: the mount appear. Rotating the DSN is the same two steps with the next id, which deploys a
    #: new revision of the service.
    sentry_dsn_version: str = ""
{%- endif %}

    def __post_init__(self) -> None:
        problems: list[str] = []
        if not re.fullmatch(r"[0-9]{12}", self.account_id):
            problems.append(f"accountId {self.account_id!r} is not 12 digits")
        if not re.fullmatch(r"[a-z]{2}(-gov)?-[a-z]+-[0-9]", self.region):
            problems.append(f"region {self.region!r} is not a region code such as eu-west-1")
        if not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", self.github_repository):
            # A wildcard here is a trust policy any repository can satisfy.
            problems.append(f"githubRepository {self.github_repository!r} is not owner/repo")
        prefix = self.github_subject_prefix
        if prefix and (
            not prefix.startswith("repo:") or re.search(r"[*? ]", prefix) or prefix.endswith(":")
        ):
            # Matched with StringEquals: a wildcard would not work, and a malformed prefix matches
            # nothing GitHub issues.
            problems.append(
                f"githubSubjectPrefix {prefix!r} is not a subject prefix such as "
                "repo:<owner>@<id>/<repo>@<id>"
            )
        if not re.fullmatch(r"[a-z0-9]([a-z0-9-]*[a-z0-9])?", self.slug):
            # ECR repository names are lowercase, and every other name here is built the same way.
            problems.append(f"slug {self.slug!r} is not lowercase letters, digits and hyphens")
        if self.app_environment not in APP_ENVIRONMENTS:
            problems.append(
                f"appEnvironment {self.app_environment!r} is not one of {APP_ENVIRONMENTS}"
            )
        if self.min_tasks < 1:
            problems.append("minTasks must be at least 1: Express Mode cannot scale to zero")
        if self.max_tasks < self.min_tasks:
            problems.append(f"maxTasks {self.max_tasks} is below minTasks {self.min_tasks}")
        if self.availability_zone_count not in AVAILABILITY_ZONE_COUNTS:
            problems.append(
                f"availabilityZoneCount {self.availability_zone_count} is not one of "
                f"{AVAILABILITY_ZONE_COUNTS}"
            )
        offered = FARGATE_MEMORY.get(self.cpu)
        if offered is None:
            problems.append(f"cpu {self.cpu!r} is not one of {tuple(FARGATE_MEMORY)}")
        elif not self.memory.isdigit() or int(self.memory) not in offered:
            problems.append(
                f"memory {self.memory!r} is not offered by Fargate at cpu {self.cpu}: "
                f"one of {offered}"
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
        if self.database_storage_gb < MIN_DATABASE_STORAGE_GB:
            problems.append(
                f"databaseStorageGb {self.database_storage_gb} is below gp3's "
                f"{MIN_DATABASE_STORAGE_GB} GB floor"
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
    def subject_prefix(self) -> str:
        """What the CI roles' trust policies put before `:environment:…` and `:pull_request`: the
        configured prefix, or the name-only form when none is configured."""
        return self.github_subject_prefix or f"repo:{self.github_repository}"

    @property
    def is_production(self) -> bool:
        """Whether this *stack* is the production one. Deletion protection, final snapshots,
        secret recovery windows and log retention hang off it, and none of them is a question
        about how the application behaves — `app_environment` is that question."""
        return self.environment == "prod"

    def name(self, *parts: str) -> str:
        """Resource names carry the environment, so two stacks can share an account safely."""
        return "-".join((*parts, self.environment))

    def role_name(self, role: str) -> str:
        """An IAM role name for `role`, trimmed to what IAM accepts.

        IAM role names are unique across the whole *account*, and every service generated into it
        wants a `deploy` role — so the name carries the slug, exactly as every other name here
        does. A long slug is trimmed, and a trimmed slug gets a digest of the untrimmed one: two
        services called `platform-billing-service-api` and `platform-billing-service-web` would
        otherwise trim into one role name, which is a 409 for whichever team applies second.
        """
        suffix = f"-{role}-{self.environment}"
        name = f"{self.slug}{suffix}"
        if len(name) <= MAX_ROLE_NAME:
            return name
        tag = f"-{_digest(self.slug, self.github_repository)}"
        head = self.slug[: MAX_ROLE_NAME - len(suffix) - len(tag)].rstrip("-")
        return f"{head}{tag}{suffix}"
{%- if cookiecutter.use_postgres == "yes" %}

    def db_identifier(self) -> str:
        """The RDS instance identifier: `<slug>-pg-<env>`, starting with a letter as RDS requires.

        A slug that starts with a digit is legal everywhere else in this stack, so it is prefixed
        here rather than refused. A long slug is trimmed with the same digest `role_name` uses.
        """
        head = self.slug if self.slug[0].isalpha() else f"db-{self.slug}"
        suffix = f"-pg-{self.environment}"
        identifier = f"{head}{suffix}"
        if len(identifier) > MAX_DB_IDENTIFIER:
            tag = f"-{_digest(self.slug, self.github_repository)}"
            head = head[: MAX_DB_IDENTIFIER - len(suffix) - len(tag)].rstrip("-")
            identifier = f"{head}{tag}{suffix}"
        return identifier
{%- endif %}

    @property
    def state_bucket(self) -> str:
        """The account-level Pulumi state bucket `infra/bootstrap/state-bucket.sh` creates.

        Shared by every service in the account and region, exactly like the GCP target's
        `<project>-pulumi-state`: see ADR-0013, and the ownership check in `teardown.sh`.
        """
        return f"pulumi-state-{self.account_id}-{self.region}"


#: The KMS alias the bootstrap script gives the state key. The stack looks the key up by it.
STATE_KEY_ALIAS = "alias/pulumi-state"

#: GitHub Actions' OIDC issuer. The bootstrap script creates the account's identity provider for
#: it; the stack only looks it up.
GITHUB_OIDC_URL = "https://token.actions.githubusercontent.com"


def load() -> StackConfig:
    config = pulumi.Config()
    return StackConfig(
        account_id=config.require("accountId"),
        region=config.require("region"),
        environment=config.require("environment"),
        github_repository=config.require("githubRepository"),
        slug=config.require("slug"),
        pulumi_project=pulumi.get_project(),
        stack=pulumi.get_stack(),
        app_environment=config.get("appEnvironment") or "staging",
        github_subject_prefix=(config.get("githubSubjectPrefix") or "").strip(),
        min_tasks=config.get_int("minTasks") or 1,
        max_tasks=config.get_int("maxTasks") or 4,
        cpu=config.get("cpu") or "256",
        memory=config.get("memory") or "512",
        availability_zone_count=config.get_int("availabilityZoneCount") or 2,
        cors_origins=parse_origins(config.get("corsOrigins") or ""),
{%- if cookiecutter.use_postgres == "yes" %}
        refresh_transport=(config.get("refreshTransport") or "body").strip(),
        database_instance_class=config.get("databaseInstanceClass") or "db.t4g.micro",
        database_storage_gb=config.get_int("databaseStorageGb") or MIN_DATABASE_STORAGE_GB,
        database_multi_az=config.get_bool("databaseMultiAz") or False,
{%- endif %}
{%- if cookiecutter.use_sentry == "yes" %}
        sentry_dsn_version=(config.get("sentryDsnVersion") or "").strip(),
{%- endif %}
    )
