"""Infrastructure unit tests. No cloud credentials, no network, no state.

Being able to assert on a stack without applying it is most of the argument for Pulumi over HCL, so
these run in the ordinary `pytest` invocation and on every pull request that touches `infra/`.

What is worth testing here is not "does Pulumi work" but the handful of settings where a silent
mistake is expensive: a database open to the internet, a Workload Identity pool any repository can
use, a service running as the default compute account, a secret value that reaches state, or a
stack whose export names have drifted from the variable names the deploy pipeline reads.

Most tests construct one component. The ones that need the composition — which environment reaches
which runner, what is mounted once a version is pinned — run `__main__.py` itself against the same
mocks, configured from `Pulumi.dev.yaml`, and read the requests the mocks recorded.
"""

from __future__ import annotations

import dataclasses
import re
import runpy
from dataclasses import dataclass
from pathlib import Path
from typing import Any

import pulumi
import pytest

HERE = Path(__file__).resolve().parent
PULUMI_DIR = HERE.parent
#: `<repo-root>/<component>/`: the folder the component was generated into.
COMPONENT_DIR = PULUMI_DIR.parent.parent

#: The component's full name, `<project>-<component>`: the base of every resource name, and the
#: Pulumi project's name.
SLUG = "{{ cookiecutter.__name }}"
#: The component's name: its folder, the prefix of its workflow files and, upper-cased, of every
#: repository variable it reads.
COMPONENT = "{{ cookiecutter.component }}"
VAR = "{{ cookiecutter.__component_var }}"
#: The image `just infra-first-deploy` pushes and records as `serviceImage`: the path CD pushes to,
#: tagged with a commit.
IMAGE = (
    f"europe-west1-docker.pkg.dev/test-project/{SLUG}-dev/{SLUG}-dev"
    ":0123456789abcdef0123456789abcdef01234567"
)


def _repository_root() -> Path:
    """The repository the component lives in: the nearest directory above it holding `.github`."""
    for directory in COMPONENT_DIR.parents:
        if (directory / ".github").is_dir():
            return directory
    raise AssertionError(f"no .github above {COMPONENT_DIR}: is this a generated repository?")


REPO = _repository_root()


@dataclass(frozen=True)
class Registered:
    typ: str
    name: str
    inputs: dict[str, Any]


class Mocks(pulumi.runtime.Mocks):
    """Records every resource the program asks for, and fills outputs in the shape GCP returns."""

    def __init__(self) -> None:
        self.resources: list[Registered] = []

    def new_resource(self, args: pulumi.runtime.MockResourceArgs) -> tuple[str, dict[str, Any]]:
        self.resources.append(Registered(args.typ, args.name, dict(args.inputs)))
        outputs = dict(args.inputs)
        if args.typ == "gcp:sql/databaseInstance:DatabaseInstance":
            outputs["connectionName"] = "test-project:europe-west1:app-pg-dev"
        if args.typ == "gcp:serviceaccount/account:Account":
            outputs["email"] = f"{args.inputs.get('accountId', 'sa')}@test.iam.gserviceaccount.com"
            outputs["name"] = f"projects/test-project/serviceAccounts/{outputs['email']}"
        return f"{args.name}-id", outputs

    def call(
        self, args: pulumi.runtime.MockCallArgs
    ) -> tuple[dict[str, Any], list[tuple[str, str]] | None]:
        if args.token == "gcp:organizations/getProject:getProject":
            return {"number": "123456789", "projectId": "test-project"}, []
        return {}, []


MOCKS = Mocks()
# The project name is what `pulumi.Config()` namespaces keys by, so the program run below reads
# `Pulumi.dev.yaml`'s keys as they are written.
pulumi.runtime.set_mocks(MOCKS, project=SLUG, stack="dev", preview=False)

from components import Identities, Secrets, Service  # noqa: E402 - must follow set_mocks
from components.secrets import SecretRef  # noqa: E402 - must follow set_mocks
from config import StackConfig  # noqa: E402 - must follow set_mocks

CONFIG = StackConfig(
    project="test-project",
    region="europe-west1",
    environment="dev",
    github_repository="{{ cookiecutter.github_repository }}",
    slug=SLUG,
)


def _stack_file() -> dict[str, str]:
    """`Pulumi.dev.yaml`'s configuration, as the strings Pulumi hands the program."""
    lines = (PULUMI_DIR / "Pulumi.dev.yaml").read_text().splitlines()
    pairs = (re.fullmatch(r"\s+([a-z0-9-]+:\w+): (.*)", line) for line in lines)
    return {m.group(1): m.group(2).strip('"') for m in pairs if m}


@dataclass(frozen=True)
class Program:
    """One run of `__main__.py`: its module globals, and every resource it asked GCP for."""

    globals: dict[str, Any]
    resources: tuple[Registered, ...]

    def of_type(self, typ: str) -> list[Registered]:
        return [r for r in self.resources if r.typ == typ]


def _program(**config: str) -> Program:
    """Run the composition root against the mocks, configured from `Pulumi.dev.yaml` plus
    `config` — stack keys without the project prefix, e.g. `jwtSecretVersion="3"`.

    After the first deploy unless told otherwise: `serviceImage` is set, so the service exists.
    `serviceImage=""` is the stack between the first `just infra-up` and `just infra-first-deploy`.
    """
    config = {"serviceImage": IMAGE} | config
    settings = _stack_file() | {f"{SLUG}:{key}": value for key, value in config.items()}
    module: dict[str, Any] = {}

    def run() -> None:
        module.update(runpy.run_path(str(PULUMI_DIR / "__main__.py")))

    MOCKS.resources.clear()
    pulumi.runtime.set_all_config(settings)
    try:
        pulumi.runtime.test(run)()
    finally:
        pulumi.runtime.set_all_config({})
    return Program(module, tuple(MOCKS.resources))


def _resolved(output: Any) -> Any:
    """The value an output resolves to under the mocks."""
    seen: list[Any] = []

    def read() -> pulumi.Output[None]:
        return pulumi.Output.from_input(output).apply(seen.append)

    pulumi.runtime.test(read)()
    return seen[0]


# --- The two environments -------------------------------------------------
#
# `environment` names resources and `app_environment` is what the application is told it is. They
# are read as one thing constantly, and the failure is quiet: a `dev` stack deploying a service
# that believes it is production serves no /docs and says nothing about why.


def test_the_stack_environment_names_resources_and_the_app_never_sees_it():
    """Two environments must be able to share one project without colliding."""
    assert CONFIG.name("api") == "api-dev"
    assert CONFIG.name(SLUG, "migrate").endswith("-migrate-dev")


def test_the_application_environment_is_configured_rather_than_hard_coded():
    """A literal here is a stack whose name says `dev` and whose service believes it is prod."""
    program = (PULUMI_DIR / "__main__.py").read_text()
    assert '"ENVIRONMENT": config.app_environment' in program
    assert CONFIG.app_environment == "staging", "a first deploy should still answer /docs"


def test_an_application_environment_the_app_cannot_parse_is_refused():
    """Caught here, or several minutes into an apply by a container that exits on startup."""
    with pytest.raises(ValueError, match="appEnvironment"):
        dataclasses.replace(CONFIG, app_environment="dev")


# --- A browser client -----------------------------------------------------
#
# The browser client is its own repository and stack (ADR-0022). What this stack owns is the two
# settings that let one call the API: which origins may, and where the refresh token travels.


def test_no_browser_may_call_the_service_until_an_origin_is_configured():
    """CORS off is the safe default: an allowlist nobody chose is a hole nobody reviewed."""
    assert CONFIG.cors_origins == ()
    assert "CORS_ORIGINS" not in CONFIG.browser_env
{%- if cookiecutter.use_postgres == "yes" %}
    assert CONFIG.refresh_transport == "body"
    assert CONFIG.browser_env == {"REFRESH_TRANSPORT": "body"}
{%- else %}
    assert CONFIG.browser_env == {}
{%- endif %}


def test_cors_origins_are_parsed_from_a_comma_separated_list():
    from config import parse_origins

    assert parse_origins("") == ()
    assert parse_origins(" https://app.example.com , ,http://localhost:3000 ") == (
        "https://app.example.com",
        "http://localhost:3000",
    )


def test_configured_origins_reach_the_service_as_one_variable():
    config = dataclasses.replace(
        CONFIG, cors_origins=("https://app.example.com", "http://localhost:3000")
    )
    assert config.browser_env["CORS_ORIGINS"] == "https://app.example.com,http://localhost:3000"


@pytest.mark.parametrize(
    "origin",
    [
        "app.example.com",
        "ftp://app.example.com",
        "https://app.example.com/",
        "https://app.example.com/app",
        "https://app.example.com?x=1",
        "https://user@app.example.com",
        "https://*.example.com",
        "*",
        "https://app.example.com:notaport",
    ],
)
def test_an_entry_that_is_not_an_origin_is_refused(origin):
    """The application refuses the same entries and exits on start-up. A browser never sends a
    path or a trailing slash in `Origin`, so such an entry is dead configuration that looks
    alive."""
    with pytest.raises(ValueError, match="corsOrigins entry"):
        dataclasses.replace(CONFIG, cors_origins=(origin,))


def test_an_origin_with_a_port_is_accepted():
    dataclasses.replace(CONFIG, cors_origins=("http://localhost:3000", "https://app.example.com"))
{%- if cookiecutter.use_postgres == "yes" %}


def test_a_cookie_transport_reaches_the_service():
    config = dataclasses.replace(
        CONFIG, cors_origins=("https://app.example.com",), refresh_transport="cookie"
    )
    assert config.browser_env == {
        "CORS_ORIGINS": "https://app.example.com",
        "REFRESH_TRANSPORT": "cookie",
    }


def test_a_transport_the_app_cannot_parse_is_refused():
    with pytest.raises(ValueError, match="refreshTransport"):
        dataclasses.replace(CONFIG, refresh_transport="header")


def test_a_cookie_transport_without_an_origin_is_refused():
    """The cookie routes' CSRF check accepts only listed origins, so the application refuses to
    start with this pair. Refused here, at preview, rather than by a revision that never starts."""
    with pytest.raises(ValueError, match="requires corsOrigins"):
        dataclasses.replace(CONFIG, refresh_transport="cookie")
{%- endif %}


def test_the_browser_settings_go_to_the_service_and_not_the_migration_job():
    """They are about HTTP, which a migration does not serve. And the application refuses
    `REFRESH_TRANSPORT=cookie` without `CORS_ORIGINS`, so a job holding one without the other is a
    job that exits on its first settings load."""
    program = _program(corsOrigins="https://app.example.com"{% if cookiecutter.use_postgres == "yes" %}, refreshTransport="cookie"{% endif %})
    backend = program.globals["BACKEND_ENV"]
    assert backend["CORS_ORIGINS"] == "https://app.example.com"
{%- if cookiecutter.use_postgres == "yes" %}
    assert backend["REFRESH_TRANSPORT"] == "cookie"
    migrate = program.globals["MIGRATE_ENV"]
    assert migrate["ENVIRONMENT"] == backend["ENVIRONMENT"], "the job still runs as the app"
    assert not {"CORS_ORIGINS", "REFRESH_TRANSPORT"} & set(migrate)
{%- endif %}


def test_the_stack_file_sets_every_browser_setting_the_program_reads():
    """A key spelled one way in `Pulumi.dev.yaml` and another in `config.py` is not an error: the
    program quietly runs on the default."""
    stack_file = (PULUMI_DIR / "Pulumi.dev.yaml").read_text()
    source = (PULUMI_DIR / "config.py").read_text()
    keys = ["corsOrigins"]
{%- if cookiecutter.use_postgres == "yes" %}
    keys += ["refreshTransport", "jwtSecretVersion", "dbAdminPasswordVersion"]
{%- endif %}
    for key in keys:
        assert f"{SLUG}:{key}:" in stack_file, key
        assert f'config.get("{key}")' in source, key


# --- Naming ---------------------------------------------------------------


def test_service_account_ids_fit_what_gcp_accepts():
    """A service-account id is 6-30 characters, and a long project name is the way past that.

    The failure without this is a mid-apply rejection on a name nobody looked at, after the APIs
    and the registry have already been created.
    """
    for account in (CONFIG.account_id(role) for role in ("run", "ci", "migrate", "apply")):
        assert 6 <= len(account) <= 30, account
        assert re.fullmatch(r"[a-z][a-z0-9-]{4,28}[a-z0-9]", account), account


def test_a_long_project_name_is_shortened_rather_than_rejected():
    long = dataclasses.replace(CONFIG, slug="a-very-long-service-name-indeed", environment="prod")
    assert len(long.account_id("run")) <= 30
    assert len(long.account_id("apply")) <= 30
    assert long.account_id("run") != long.account_id("ci") != long.account_id("apply")


def test_the_pool_id_is_unique_within_the_project_not_just_the_stack():
    """Two services in one GCP project must not both want the same pool.

    A pool id is project-global. Building it from the kind and the environment alone — `github-dev`
    for everybody — means the second service generated from this template fails `pulumi up` with a
    409 on a pool the first one owns, and the id cannot be freed for 30 days.
    """
    other = dataclasses.replace(CONFIG, slug="another-service")
    assert CONFIG.pool_id("github") != other.pool_id("github")
    assert CONFIG.pool_id("github").startswith(SLUG[:8])


def test_two_long_slugs_sharing_a_prefix_do_not_trim_into_one_name():
    """Trimming is where two distinct services become one name, and 32 characters is not many.

    `platform-billing-service-api` and `platform-billing-service-web` both cut down to
    `platform-billing-serv`, which is a 409 for whichever team deploys second and a name neither
    can free for 30 days. The digest is taken over the untrimmed slug, so what the trim discards
    still reaches the result.
    """
    api = dataclasses.replace(CONFIG, slug="platform-billing-service-api")
    web = dataclasses.replace(CONFIG, slug="platform-billing-service-web")
    assert api.pool_id("github") != web.pool_id("github")


def test_the_same_slug_in_two_repositories_is_two_pools():
    """Two teams both calling their service `orders` in one project must not fight over a name."""
    ours = dataclasses.replace(CONFIG, slug="orders", github_repository="acme/orders")
    theirs = dataclasses.replace(CONFIG, slug="orders", github_repository="other-team/orders")
    assert ours.pool_id("github") != theirs.pool_id("github")


def test_the_pool_id_is_the_same_every_time_it_is_computed():
    """A random suffix would be more unique and unusable.

    Pulumi would see a new pool id on every run, replace the pool every time, and drop the old id
    into GCP's 30-day hold on each replacement. Determinism is the requirement, not an optimisation.
    """
    assert CONFIG.pool_id("github") == CONFIG.pool_id("github")


def test_a_long_project_name_still_yields_a_pool_id_gcp_accepts():
    long = dataclasses.replace(CONFIG, slug="a-very-long-service-name-indeed", environment="prod")
    pool = long.pool_id("github")
    assert 4 <= len(pool) <= 32, pool
    assert re.fullmatch(r"[a-z][a-z0-9-]{2,30}[a-z0-9]", pool), pool


# --- Security -------------------------------------------------------------


@pulumi.runtime.test
def test_workload_identity_is_scoped_to_one_repository():
    """Without the attribute condition, any repository on GitHub can assume this identity."""
    identities = Identities(CONFIG, project_number="123456789")
    expected = 'assertion.repository == "{{ cookiecutter.github_repository }}"'
    return identities.provider.attribute_condition.apply(lambda cond: cond == expected)


def test_only_the_infrastructure_environment_may_impersonate_the_apply_account():
    """The apply account can change anything in the project. What stands between it and a pull
    request's preview, the deploy job or any other workflow is the environment its token names —
    mapped from GitHub's `environment` claim — and that environment's protection rules."""
    from components.identities import APPLY_ENVIRONMENT

    program = _program()
    provider = next(
        r
        for r in program.of_type(
            "gcp:iam/workloadIdentityPoolProvider:WorkloadIdentityPoolProvider"
        )
    )
    assert provider.inputs["attributeMapping"]["attribute.environment"] == "assertion.environment"
    members = {r.name: r.inputs for r in program.of_type("gcp:serviceaccount/iAMMember:IAMMember")}
    binding = members["github-impersonates-apply"]
    assert binding["role"] == "roles/iam.workloadIdentityUser"
    assert binding["member"].startswith("principalSet://iam.googleapis.com/projects/123456789/")
    assert binding["member"].endswith("/attribute.environment/infrastructure")
    assert APPLY_ENVIRONMENT == "infrastructure", "infra.yml's apply job runs in `infrastructure`"
    account = f"{CONFIG.account_id('apply')}@test.iam.gserviceaccount.com"
    assert binding["serviceAccountId"].endswith(f"/{account}")


def test_the_apply_account_holds_predefined_roles_and_the_state_it_writes():
    """Basic roles are ruled out in production by the GCP infrastructure standards. The apply
    account gets one admin role per service the stack manages, on the project, and administers
    the state bucket and key on those two resources alone."""
    from components.identities import APPLY_ROLES, CI_ROLES

    assert not {"roles/owner", "roles/editor", "roles/viewer"} & set(APPLY_ROLES)
    assert "roles/iam.serviceAccountUser" not in APPLY_ROLES, "granted per account, below"
    assert not set(APPLY_ROLES) & set(CI_ROLES), "CI deploys and previews; it gains no admin role"
    program = _program()
    apply = f"serviceAccount:{CONFIG.account_id('apply')}@test.iam.gserviceaccount.com"
    granted = {
        r.inputs["role"]
        for r in program.of_type("gcp:projects/iAMMember:IAMMember")
        if r.inputs["member"] == apply
    }
    assert granted == set(APPLY_ROLES)

    (state,) = (
        r.inputs
        for r in program.of_type("gcp:storage/bucketIAMMember:BucketIAMMember")
        if r.inputs["member"] == apply
    )
    assert state["bucket"] == "{{ cookiecutter.gcp_project_id }}-pulumi-state"
    assert state["role"] == "roles/storage.admin"
    keys = {
        r.inputs["role"]: r.inputs["cryptoKeyId"]
        for r in program.of_type("gcp:kms/cryptoKeyIAMMember:CryptoKeyIAMMember")
        if r.inputs["member"] == apply
    }
    assert set(keys) == {"roles/cloudkms.admin", "roles/cloudkms.cryptoKeyEncrypterDecrypter"}
    assert all(key.endswith("/keyRings/pulumi/cryptoKeys/stack") for key in keys.values())

    # It creates Cloud Run resources that run as these accounts, so it acts as each — per account.
    members = {r.name: r.inputs for r in program.of_type("gcp:serviceaccount/iAMMember:IAMMember")}
    acts_as = {"runtime": "run"}
{%- if cookiecutter.use_postgres == "yes" %}
    acts_as["migrator"] = "migrate"
{%- endif %}
    for label, role in acts_as.items():
        member = members[f"apply-acts-as-{label}"]
        assert member["role"] == "roles/iam.serviceAccountUser"
        assert member["member"] == apply
        assert member["serviceAccountId"].endswith(
            f"/{CONFIG.account_id(role)}@test.iam.gserviceaccount.com"
        )


def test_ci_holds_no_project_wide_write_or_secret_read_role():
    """CI can deploy and push images. It must not be able to read a secret's value or rewrite
    state outside the one bucket it needs — both are granted per resource, not per project."""
    from components.identities import CI_ROLES

    forbidden = (
        "roles/owner",
        "roles/editor",
        "roles/storage.admin",
        "roles/storage.objectAdmin",
        "roles/storage.objectUser",
        "roles/secretmanager.secretAccessor",
        "roles/secretmanager.admin",
    )
    for role in CI_ROLES:
        assert role not in forbidden, f"{role} is project-wide; it belongs scoped to one resource"
    # The pull-request preview runs with --refresh, which reads every resource and every IAM
    # binding the stack declares. `viewer` covers resource state; it excludes getIamPolicy.
    assert "roles/viewer" in CI_ROLES
    assert "roles/iam.securityReviewer" in CI_ROLES


def test_the_runtime_account_cannot_deploy():
    from components.identities import RUNTIME_ROLES

    for role in ("roles/run.developer", "roles/run.admin", "roles/artifactregistry.writer"):
        assert role not in RUNTIME_ROLES
{%- if cookiecutter.use_postgres == "yes" %}


def test_the_runtime_account_logs_in_to_the_database_as_itself():
    """`cloudsql.client` reaches the instance; `cloudsql.instanceUser` is the IAM login. Neither
    reads a row: the tables are the migration's grants to the account's database user."""
    from components.identities import RUNTIME_ROLES

    assert {"roles/cloudsql.client", "roles/cloudsql.instanceUser"} <= set(RUNTIME_ROLES)
    assert "roles/cloudsql.admin" not in RUNTIME_ROLES


def test_the_migrator_account_reaches_the_database_and_nothing_else():
    """It connects as `postgres` with a password it alone can read, so it needs no IAM login, and
    it neither deploys nor reads any other secret — that access is granted per secret."""
    from components.identities import MIGRATOR_ROLES

    assert set(MIGRATOR_ROLES) == {"roles/logging.logWriter", "roles/cloudsql.client"}


def test_ci_may_deploy_the_migration_job_as_its_own_account():
    """`gcloud run jobs update --image` needs `actAs` on the account the job runs as. Without the
    grant the migrate step fails before anything has run, with an error that names no account."""
    members = {
        r.name: r.inputs for r in _program().of_type("gcp:serviceaccount/iAMMember:IAMMember")
    }
    ci = f"serviceAccount:{CONFIG.account_id('ci')}@test.iam.gserviceaccount.com"
    for label, role in (("migrator", "migrate"), ("runtime", "run")):
        member = members[f"ci-acts-as-{label}"]
        assert member["role"] == "roles/iam.serviceAccountUser"
        assert member["member"] == ci
        account = f"{CONFIG.account_id(role)}@test.iam.gserviceaccount.com"
        assert member["serviceAccountId"].endswith(f"/{account}")
{%- endif %}


@pulumi.runtime.test
def test_the_service_runs_as_the_runtime_account_not_the_default():
    """The default compute account holds Editor on the whole project."""
    return _service().service.template.apply(
        lambda template: template.service_account == "runtime@test.iam.gserviceaccount.com"
    )


def test_a_service_refuses_a_secret_it_was_never_given():
    """Otherwise the failure is a revision that cannot resolve its secret, which reads as a broken
    application rather than a mistyped key."""
    with pytest.raises(KeyError, match="typo"):
        Service(
            CONFIG,
            app=SLUG,
            image=IMAGE,
            runtime_email="runtime@test.iam.gserviceaccount.com",
            port=8080,
            health_path="/health/live",
            secret_env={"JWT_SECRET": "typo"},
            secret_refs={"jwt-secret": SecretRef(secret="x", version="1")},
        )


@pulumi.runtime.test
def test_a_mounted_secret_is_pinned_to_a_version_number():
    """Cloud Run reads an environment secret when an instance starts, so `latest` lets the
    instances of one revision hold different values after a rotation. Pinned, a rotation is a new
    revision and a rollback restores the credential with the image."""
    assert '"latest"' not in (PULUMI_DIR / "components" / "service.py").read_text()

    def check(template):
        ref = template.containers[0].envs[-1].value_source.secret_key_ref
        assert ref.version == "7"
        return True

    return _service(
        secret_env={"JWT_SECRET": "jwt-secret"},
        secret_refs={"jwt-secret": SecretRef(secret="jwt-secret-dev", version="7")},
    ).service.template.apply(check)


def test_every_secret_is_an_empty_container_one_account_may_read():
    """Pulumi standards MUST-3, ADR-0023: the stack creates the container and grants access; the
    value is added out of band. So no version is created here, and each container is readable by
    the one account named for it rather than by everything that can read the others."""
    holder: dict[str, Secrets] = {}

    def construct() -> None:
        holder["secrets"] = Secrets(
            CONFIG,
            accessors={
                "jwt-secret": "runtime@test.iam.gserviceaccount.com",
                "db-admin-password": "migrator@test.iam.gserviceaccount.com",
            },
        )

    MOCKS.resources.clear()
    pulumi.runtime.test(construct)()
    resources = list(MOCKS.resources)
    secrets = holder["secrets"]

    assert not [r for r in resources if r.typ == "gcp:secretmanager/secretVersion:SecretVersion"]
    assert secrets.refs == {}, "nothing is mountable until its version is configured"
    grants = {
        r.inputs["secretId"]: r.inputs["member"]
        for r in resources
        if r.typ == "gcp:secretmanager/secretIamMember:SecretIamMember"
    }
    assert grants == {
        f"{SLUG}-jwt-secret-dev": "serviceAccount:runtime@test.iam.gserviceaccount.com",
        f"{SLUG}-db-admin-password-dev": "serviceAccount:migrator@test.iam.gserviceaccount.com",
    }
    assert len(secrets.grants) == 2


def test_a_secret_is_pinned_to_its_configured_version_and_nothing_else():
    holder: dict[str, Secrets] = {}
    pulumi.runtime.test(
        lambda: holder.setdefault(
            "secrets",
            Secrets(
                CONFIG,
                accessors={"jwt-secret": "runtime@test.iam.gserviceaccount.com"},
                versions={"jwt-secret": "3"},
            ),
        )
    )()
    assert holder["secrets"].refs["jwt-secret"].version == "3"


def test_no_secret_value_is_in_the_program_its_configuration_or_its_state():
    """Pulumi standards MUST-3. Nothing in this program generates, holds or writes a secret value:
    no secret version, no random password, no password on a database user. What reaches state is
    a container's id and a version number."""
    sources = [PULUMI_DIR / "__main__.py", *(PULUMI_DIR / "components").glob("*.py")]
    for source in sources:
        text = source.read_text()
        for forbidden in ("SecretVersion(", "secret_data", "pulumi_random", "RandomPassword"):
            assert forbidden not in text, f"{source.name}: {forbidden}"
        assert not re.search(r"\bpassword\s*=", text), source.name
    assert "pulumi-random" not in (PULUMI_DIR / "pyproject.toml").read_text()

    program = _program({% if cookiecutter.use_postgres == "yes" %}jwtSecretVersion="3", dbAdminPasswordVersion="2"{% endif %})
    types = {r.typ for r in program.resources}
    assert "gcp:secretmanager/secretVersion:SecretVersion" not in types
    assert not [t for t in types if t.startswith("random:")]
    for user in program.of_type("gcp:sql/user:User"):
        assert "password" not in user.inputs, user.name


def test_nothing_is_mounted_until_its_version_is_pinned():
    """A mounted container with no versions is a deploy that fails outright. Unpinned, the service
    starts with no mount at all{% if cookiecutter.use_postgres == "yes" %} — and refuses to serve without a signing key, which is why
    `sync-github.sh` will not un-gate CD until `secrets_ready` says so{% endif %}."""
    assert _program().globals["SECRET_ENV"] == {}
{%- if cookiecutter.use_postgres == "yes" %}
    pinned = _program(jwtSecretVersion="3", dbAdminPasswordVersion="2")
    assert pinned.globals["SECRET_ENV"] == {"JWT_SECRET": "jwt-secret"}
    assert pinned.globals["MIGRATE_SECRET_ENV"] == {"DATABASE_PASSWORD": "db-admin-password"}
    refs = pinned.globals["secrets"].refs
    assert (refs["jwt-secret"].version, refs["db-admin-password"].version) == ("3", "2")
{%- endif %}


def test_secrets_are_ready_only_when_every_required_version_is_pinned():
    """The export `sync-github.sh` gates on. A string, like every other export."""
{%- if cookiecutter.use_postgres == "yes" %}
    assert not CONFIG.secrets_ready
    assert not dataclasses.replace(CONFIG, jwt_secret_version="3").secrets_ready
    assert not dataclasses.replace(CONFIG, db_admin_password_version="2").secrets_ready
    ready = dataclasses.replace(CONFIG, jwt_secret_version="3", db_admin_password_version="2")
    assert ready.secrets_ready
{%- else %}
    # No database means no users, so no signing key, and no migration: nothing to wait for.
    assert CONFIG.secrets_ready
{%- endif %}
    program = (PULUMI_DIR / "__main__.py").read_text()
    assert 'pulumi.export("secrets_ready", "true" if config.secrets_ready else "false")' in program
{%- if cookiecutter.use_postgres == "yes" %}


def test_the_migration_job_is_not_granted_the_signing_key():
    """It signs nothing. A job holding a credential it never uses is a credential with a second
    place to leak from, and this one is the whole of the auth system."""
    program = _program(jwtSecretVersion="3", dbAdminPasswordVersion="2")
    assert "JWT_SECRET" not in program.globals["MIGRATE_SECRET_ENV"]
    assert "jwt-secret" not in program.globals["MIGRATE_SECRET_ENV"].values()


def test_the_admin_password_is_readable_by_the_migration_job_alone():
    """The service logs in as itself and has no use for the admin login; a compromised service
    that could read it would own the schema."""
    grants = {
        r.inputs["secretId"]: r.inputs["member"]
        for r in _program().of_type("gcp:secretmanager/secretIamMember:SecretIamMember")
    }
    migrator = f"serviceAccount:{CONFIG.account_id('migrate')}@test.iam.gserviceaccount.com"
    runtime = f"serviceAccount:{CONFIG.account_id('run')}@test.iam.gserviceaccount.com"
    assert grants[f"{SLUG}-db-admin-password-dev"] == migrator
    assert grants[f"{SLUG}-jwt-secret-dev"] == runtime
{%- endif %}


# --- Cloud Run shape ------------------------------------------------------


def _service(**overrides: Any) -> Service:
    kwargs: dict[str, Any] = {
        "app": SLUG,
        "image": IMAGE,
        "runtime_email": "runtime@test.iam.gserviceaccount.com",
        "port": 8080,
        "health_path": "/health/live",
        "env": {"ENVIRONMENT": "production"},
    }
    kwargs.update(overrides)
    return Service(CONFIG, **kwargs)


@pulumi.runtime.test
def test_the_startup_probe_reads_liveness_and_the_container_port():
    """A startup probe pointed at readiness never lets a first revision up, because readiness can
    legitimately be false until something else has run."""

    def check(template):
        container = template.containers[0]
        assert container.startup_probe.http_get.path == "/health/live"
        assert container.startup_probe.http_get.port == 8080
        assert container.ports.container_port == 8080
        return True

    return _service().service.template.apply(check)


@pulumi.runtime.test
def test_idle_cpu_throttling_comes_with_the_startup_boost():
    """Without the boost, throttling applies during startup too and a cold import graph does not
    finish inside the probe budget. The symptom is an intermittently failing deploy."""

    def check(template):
        resources = template.containers[0].resources
        assert resources.cpu_idle is True
        assert resources.startup_cpu_boost is True
        return True

    return _service().service.template.apply(check)


@pulumi.runtime.test
def test_a_service_mounts_no_cloud_sql_socket():
    """A service reaches its database through the language connector as its own IAM user, so a
    `/cloudsql` attachment would be unused — and an unused attachment is a permission the service
    does not need. Only a job opts in, with `JobSpec.cloud_sql_instance`."""
    source = (PULUMI_DIR / "components" / "service.py").read_text()
    assert "ServiceTemplateVolumeCloudSqlInstanceArgs" not in source

    def check(template):
        assert not template.volumes
        assert not template.containers[0].volume_mounts
        return True

    return _service().service.template.apply(check)


def test_the_pipeline_owns_the_image_and_nothing_else_does():
    """Two tools writing one field is how an infrastructure repository stops being trusted."""
    source = (PULUMI_DIR / "components" / "service.py").read_text()
    assert 'ignore_changes=["template.containers[0].image"' in source
    assert '"template.template.containers[0].image"' in source


# --- The first deploy -----------------------------------------------------


def test_nothing_that_runs_exists_before_the_first_deploy():
    """There is no placeholder image. Until `serviceImage` is set the stack creates everything a
    service needs and no service{% if cookiecutter.use_postgres == "yes" %} and no migration job{% endif %} — and exports them empty, which is what keeps
    `sync-github.sh` from un-gating CD."""
    before = _program(serviceImage="")
    assert not before.of_type("gcp:cloudrunv2/service:Service")
    assert not before.of_type("gcp:cloudrunv2/job:Job")
    assert before.globals["backend"] is None
    assert before.of_type("gcp:artifactregistry/repository:Repository"), "the registry exists"
    source = (PULUMI_DIR / "__main__.py").read_text()
    assert 'pulumi.export("service_url", backend.url if backend else "")' in source
    assert 'pulumi.export("cloud_run_service", backend.service.name if backend else "")' in source


def test_the_first_deploy_creates_the_service{% if cookiecutter.use_postgres == "yes" %}_and_the_migration_job{% endif %}_on_the_image_it_built():
    """The real build, from the first apply that creates anything that runs."""
    program = _program()
    (service,) = program.of_type("gcp:cloudrunv2/service:Service")
    assert service.inputs["template"]["containers"][0]["image"] == IMAGE
{%- if cookiecutter.use_postgres == "yes" %}
    (job,) = program.of_type("gcp:cloudrunv2/job:Job")
    assert job.inputs["template"]["template"]["containers"][0]["image"] == IMAGE
{%- endif %}


def test_a_service_image_that_is_not_pinned_is_refused():
    """`:latest`, or no tag at all, is a service created on whatever it pointed at that day."""
    for image in ("nginx", "europe-west1-docker.pkg.dev/p/r x/s:1"):
        with pytest.raises(ValueError, match="serviceImage"):
            dataclasses.replace(CONFIG, service_image=image)
    dataclasses.replace(CONFIG, service_image=IMAGE)


def _first_deploy_script() -> str:
    return (PULUMI_DIR.parent / "scripts" / "first-deploy.sh").read_text()


def test_the_first_deploy_builds_what_cd_builds_where_cd_pushes_it():
    """The same Dockerfile, target, build argument and image path, for the platform the service
    runs on — from a clean checkout, because the image is named for a commit."""
    script = _first_deploy_script()
    cd = _workflow("cd.yml")
    assert f"file: {COMPONENT}/backend/docker/Dockerfile.prod" in cd
    assert '--file "${BACKEND_DIR}/docker/Dockerfile.prod"' in script
    assert "target: production" in cd and "--target production" in script
    assert "build-args: GIT_SHA=${{ '{{' }} github.sha }}" in cd
    assert '--build-arg "GIT_SHA=${SHA}"' in script
    assert "platforms: linux/amd64" in cd and "docker buildx build" in script
    assert "--platform linux/amd64" in script
    # cd.yml pushes `IMAGE_REPO/CLOUD_RUN_SERVICE:<sha>`; before the service exists the script
    # names it as the stack does.
    repo = "${{ '{{' }} vars." + VAR + "_IMAGE_REPO }}"
    service = "${{ '{{' }} vars." + VAR + "_CLOUD_RUN_SERVICE }}"
    assert f"IMAGE: {repo}/{service}:" in cd
    assert 'SERVICE="${SLUG}-${ENVIRONMENT}"' in script
    assert 'IMAGE="${IMAGE_REPO}/${SERVICE}:${SHA}"' in script
    assert CONFIG.name(CONFIG.slug) == f"{SLUG}-dev"
    assert "name=config.name(app)," in (PULUMI_DIR / "components" / "service.py").read_text()
    assert "status --porcelain -- . ':(exclude)infra'" in script, "it refuses a dirty component"
    assert 'config set --plaintext serviceImage "${IMAGE}"' in script
    assert 'config.get("serviceImage")' in (PULUMI_DIR / "config.py").read_text()
    assert "pulumi up --stack" in script and "--refresh" in script


def test_the_first_deploy_refuses_what_is_not_a_first_deploy():
    script = _first_deploy_script()
    assert "output image_repo" in script, "the stack has been applied"
    assert '"$(output secrets_ready)" != "true"' in script
    assert '-n "$(output service_url)"' in script, "and has no service yet"
    assert script.index("output service_url") < script.index("docker buildx build")


def test_the_first_deploy_migrates_as_cd_does_and_smoke_tests_both_probes():
    script = _first_deploy_script()
{%- if cookiecutter.use_postgres == "yes" %}
    assert "gcloud run jobs execute" in _workflow("cd.yml")
    assert 'gcloud run jobs execute "${JOB}"' in script and "--wait" in script
    assert script.index("jobs execute") < script.index('"${URL}/health/ready"')
{%- else %}
    assert "jobs execute" not in script, "no database, nothing to migrate"
{%- endif %}
    assert "/health/live" in script and "/health/ready" in script


def test_the_justfile_runs_the_first_deploy_and_passes_flags_through():
    justfile = (COMPONENT_DIR / "justfile").read_text()
    assert 'infra-first-deploy STACK="dev" *FLAGS:' in justfile
    assert "./infra/scripts/first-deploy.sh {{ '{{' }}STACK}} {{ '{{' }}FLAGS}}" in justfile


# --- APIs -----------------------------------------------------------------


def test_the_bootstrap_script_enables_what_the_stack_cannot():
    """Enabling an API is itself a `serviceusage` call authorised through `cloudresourcemanager`,
    and the provider reads the default region and zone from `compute` before planning anything. If
    one of those drifts out of the bootstrap script, the first `pulumi up` on a fresh project fails
    with `SERVICE_DISABLED` and no useful resource to point at."""
    from components.apis import BOOTSTRAP, REQUIRED

    script = (PULUMI_DIR.parent / "bootstrap" / "state-bucket.sh").read_text()
    for api in BOOTSTRAP:
        assert api in script, f"{api} must be enabled by the bootstrap script"
    # Enabling an API from two places is a race on a fresh project and a confusing diff after.
    assert not set(BOOTSTRAP) & set(REQUIRED)


# --- Teardown -------------------------------------------------------------


def test_the_teardown_script_names_what_the_bootstrap_script_created():
    """`pulumi destroy` cannot reach the state bucket or the KMS key — the stack never created
    them — so teardown.sh is the only thing that ever removes them. If its names drift from the
    ones state-bucket.sh created, it deletes nothing, exits 0, and the resources keep billing."""
    from components.identities import KEY, KEYRING, STATE_BUCKET_SUFFIX

    bootstrap = PULUMI_DIR.parent / "bootstrap"
    for source in (
        (bootstrap / "state-bucket.sh").read_text(),
        (bootstrap / "teardown.sh").read_text(),
    ):
        assert STATE_BUCKET_SUFFIX in source
        assert f'KEYRING="{KEYRING}"' in source
        assert f'KEY="{KEY}"' in source


def test_the_teardown_script_cannot_strand_a_stack():
    """Deleting the state bucket while any stack still holds resources leaves those resources
    running, billing, and with no record of what they are. Checking rather than warning is the
    whole reason this is a script and not three gcloud lines in a README, and the confirmation is
    the same bet `pulumi up` makes: refuse rather than guess when nothing can answer."""
    script = (PULUMI_DIR.parent / "bootstrap" / "teardown.sh").read_text()
    assert "stack export" in script, "the emptiness check is the guard; do not remove it"
    assert "stack ls --all" in script, (
        "without --all, `stack ls` returns only this Pulumi project's stacks and another "
        "project's live stack is absent rather than under-counted — see ADR-0013"
    )
    assert "! -t 0" in script, "must refuse when nothing can confirm"
    assert '"${reply}" != "${BUCKET}"' in script, "must make a human type the bucket name"


def test_the_teardown_script_cannot_delete_a_backend_it_shares():
    """Pulumi standards MUST-8. `state-bucket.sh` derives the bucket from the GCP project alone —
    `gs://${PROJECT}-pulumi-state`, no per-service component — so a second service generated into
    the same project bootstraps onto the same bucket by construction. One backend, a directory per
    Pulumi project under `.pulumi/stacks/`, and deleting the bucket deletes all of them.

    The emptiness check above cannot see that case and this one does not rely on it: ownership is
    a different question from resource count, because another project's stacks can hold nothing
    today and be applied tomorrow.
    """
    script = (PULUMI_DIR.parent / "bootstrap" / "teardown.sh").read_text()
    assert "/.pulumi/stacks/" in script, (
        "ownership is established by listing the backend's project directories"
    )
    assert "PULUMI_PROJECT" in script, "the listing has to be compared against this project's name"
    stacks_at = script.index("/.pulumi/stacks/")
    assert stacks_at < script.index("gcloud storage rm"), (
        "the backend listing must happen before anything is deleted"
    )


def test_every_service_the_stack_uses_has_its_api_enabled():
    """A missing API surfaces as a mid-apply failure with half the stack created."""
    from components.apis import BOOTSTRAP, REQUIRED

    enabled = set(BOOTSTRAP) | set(REQUIRED)
    needed = {
        "run.googleapis.com",
        "artifactregistry.googleapis.com",
        "secretmanager.googleapis.com",
        "iam.googleapis.com",
        "iamcredentials.googleapis.com",
        "sts.googleapis.com",
        "compute.googleapis.com",
        "cloudresourcemanager.googleapis.com",
        "serviceusage.googleapis.com",
    }
    assert needed <= enabled, f"missing: {sorted(needed - enabled)}"


# --- The contract with the deploy pipeline --------------------------------
#
# The failure this section exists to prevent: a stack exporting `workload_identity_provider` and a
# pipeline reading `vars.WIF_PROVIDER`. Nothing errors. The deploy job is simply skipped forever,
# because it is gated on a variable nothing ever sets.

#: Repository variables a human sets by hand, because they come from outside this stack.
MANUAL_VARIABLES = {
    f"{VAR}_SENTRY_ORG",
    f"{VAR}_SENTRY_PROJECT",
}

#: Every workflow that deploys something. Each one is a fresh chance for a variable name to drift
#: from the stack that is supposed to supply it, so each one is checked.
DEPLOY_WORKFLOWS = ("cd.yml",)

#: Every workflow the component installs, by the name it had in the template.
WORKFLOWS = ("ci.yml", "cd.yml", "infra.yml")


def _workflow(name: str) -> str:
    """Where the generated repository keeps it: the root's `.github/workflows/`, prefixed."""
    return (REPO / ".github" / "workflows" / f"{COMPONENT}-{name}").read_text()


def _sync_script() -> str:
    return (PULUMI_DIR.parent / "scripts" / "sync-github.sh").read_text()


def test_every_variable_a_deploy_reads_is_one_the_sync_script_sets():
    provided = set(re.findall(r"gh variable set \"?([A-Z0-9_]+)", _sync_script()))
    provided |= set(re.findall(r'"[a-z_]+:([A-Z0-9_]+)"', _sync_script()))
    for name in (*DEPLOY_WORKFLOWS, "infra.yml"):
        referenced = set(re.findall(r"vars\.([A-Z0-9_]+)", _workflow(name))) - MANUAL_VARIABLES
        assert referenced <= provided, f"{name}: never set: {sorted(referenced - provided)}"


def test_every_output_the_sync_script_reads_is_one_the_stack_exports():
    mapped = set(re.findall(r'"([a-z_]+):[A-Z0-9_]+"', _sync_script()))
    program = (PULUMI_DIR / "__main__.py").read_text()
    exported = set(re.findall(r'pulumi\.export\(\s*"([a-z_]+)"', program))
    assert mapped <= exported, f"never exported: {sorted(mapped - exported)}"


def _not_synced() -> set[str]:
    body = re.search(r"declare -a NOT_SYNCED=\(([^)]*)\)", _sync_script())
    assert body
    return set(body.group(1).split())


def test_every_export_is_synced_or_deliberately_not():
    """An export nobody syncs and nobody listed is either dead or a variable somebody forgot."""
    mapped = set(re.findall(r'"([a-z_]+):[A-Z0-9_]+"', _sync_script()))
    # Synced outside the mapping, because an empty value is its normal state without a database.
    mapped.add("cloud_run_migrate_job")
    assert f"gh variable set {VAR}_CLOUD_RUN_MIGRATE_JOB" in _sync_script()
    program = (PULUMI_DIR / "__main__.py").read_text()
    exported = set(re.findall(r'pulumi\.export\(\s*"([a-z_]+)"', program))
    assert _not_synced() == {"secrets_ready", "cloud_sql_instance"}
    assert exported == mapped | _not_synced()
    assert not mapped & _not_synced()


def test_cd_is_not_ungated_until_every_secret_has_a_pinned_version():
    """A deploy before then is a revision that refuses to start — no signing key — or a migration
    with no password. The script checks the stack's own answer first, and names the fix."""
    script = _sync_script()
    assert "read_output secrets_ready" in script
    assert '"${SECRETS_READY}" != "true"' in script
    assert "just infra-secrets" in script
    assert script.index("secrets_ready") < script.index("gh variable set")


def test_cd_is_not_ungated_until_the_first_deploy_has_created_the_service():
    script = _sync_script()
    assert "read_output service_url" in script
    assert "just infra-first-deploy" in script
    assert script.index("service_url") < script.index("gh variable set")


def test_every_variable_the_sync_script_writes_is_the_components():
    """A second component's sync must not overwrite this one's configuration."""
    script = _sync_script()
    written = set(re.findall(r'"[a-z_]+:([A-Z0-9_]+)"', script))
    written |= set(re.findall(r"gh variable set \"?([A-Z0-9_]+)", script))
    assert written
    for variable in written:
        assert variable.startswith(f"{VAR}_"), variable
    assert not re.search(r"(?<![A-Z_])SENTRY_(ORG|PROJECT|AUTH_TOKEN)", script)
{%- if cookiecutter.use_postgres == "yes" %}


def _secrets_script() -> str:
    return (PULUMI_DIR.parent / "scripts" / "secrets.sh").read_text()


def test_the_secrets_script_names_and_pins_what_the_stack_reads():
    """It fills the containers the stack created and sets the keys `config.py` reads. A name that
    drifts on either side is a version added to a secret nothing mounts."""
    script = _secrets_script()
    assert 'echo "${SLUG}-$1-${ENVIRONMENT}"' in script
    assert CONFIG.name(CONFIG.slug, "jwt-secret") == f"{SLUG}-jwt-secret-dev"
    source = (PULUMI_DIR / "config.py").read_text()
    pins = (("jwt-secret", "jwtSecretVersion"), ("db-admin-password", "dbAdminPasswordVersion"))
    for name, key in pins:
        assert f"fill {name} {key}" in script, name
        assert f'config.get("{key}")' in source, key
    justfile = (COMPONENT_DIR / "justfile").read_text()
    assert "infra-secrets STACK" in justfile
    assert "./infra/scripts/secrets.sh" in justfile


def test_the_secrets_script_never_puts_a_value_where_it_can_be_read():
    """Not printed, not an argument, not a file: generated into a variable and handed on stdin by
    `printf`, a builtin, so it never reaches a process list."""
    script = _secrets_script()
    uses = [line.strip() for line in script.splitlines() if "${value}" in line]
    assert uses, "the script must use the value somewhere"
    for line in uses:
        assert line.startswith(
            ("printf '%s' \"${value}\" |", 'version="$(printf \'%s\' "${value}" |')
        ), line
    assert "--data-file=-" in script
    assert "set -x" not in script
    assert "--rotate" in script and "signs every user out" in script


def test_the_secrets_script_pins_versions_as_plaintext():
    """A version is not a secret, and the program has to read it to pin exactly that one."""
    assert 'config set --plaintext "$1" "$2"' in _secrets_script()
{%- endif %}


def test_every_deploy_stays_inert_until_it_is_configured():
    """A committed deploy path that cannot fire by accident is reviewed alongside the code it
    ships, rather than written under pressure on the day."""
    for name in DEPLOY_WORKFLOWS:
        assert f"if: vars.{VAR}_WIF_PROVIDER != ''" in _workflow(name), name
    assert f"vars.{VAR}_WIF_PROVIDER != ''" in _workflow("infra.yml")
    assert f"vars.{VAR}_APPLY_SA != ''" in _workflow("infra.yml")


def test_every_merge_to_main_is_applied_by_ci_as_the_apply_account():
    """Pulumi standards MUST-9: after the environment is set up from a machine, every apply runs
    from CI. Only a token from one GitHub environment may impersonate the apply account, so the
    job has to run in it; it has to refresh, or it rolls the service back to the image the state
    last saw; and it must never be cancelled halfway through."""
    apply = _jobs(_workflow("infra.yml"))["apply"]
    for want in (
        "github.event_name == 'push' && github.ref == 'refs/heads/main'",
        "environment: infrastructure",
        "service_account:",
        f"vars.{VAR}_APPLY_SA",
        "command: up",
        "stack-name: dev",
        "refresh: true",
        f"group: {COMPONENT}-infra-apply",
        "cancel-in-progress: false",
        "needs: [static]",
    ):
        assert want in apply, want
    assert "DEPLOY_SA" not in apply, "the apply impersonates the apply account, not CI's"
    assert apply.index("actions/checkout") < apply.index("pulumi/actions"), "checkout comes first"


def test_only_the_preview_runs_on_a_pull_request_and_no_push_is_cancelled():
    """The preview is the review; the apply account is never in reach of a pull request. A push
    to main queues, never cancels: cancelling one could stop an apply halfway through."""
    infra = _workflow("infra.yml")
    preview = _jobs(infra)["preview"]
    assert "github.event_name == 'pull_request'" in preview and "command: preview" in preview
    assert "APPLY_SA" not in preview and "command: up" not in preview
    assert "cancel-in-progress: true" not in infra


def test_every_deploy_is_filtered_to_the_component_it_deploys():
    """Without a filter, a typo fix in a README rebuilds and redeploys a container."""
    for name in DEPLOY_WORKFLOWS:
        assert "paths:" in _workflow(name), name
    # The contract test reads the deploy workflows, so a change to one has to run it.
    infra = _workflow("infra.yml")
    assert f"'.github/workflows/{COMPONENT}-cd.yml'" in infra
    assert f"'{COMPONENT}/infra/**'" in infra
    for name in WORKFLOWS:
        workflow = _workflow(name)
        # Its own folder, and its own file under the name it was installed as.
        assert f"'.github/workflows/{COMPONENT}-{name}'" in workflow, name
        if name != "infra.yml":
            assert f"'{COMPONENT}/**'" in workflow, name


def test_every_deploy_tags_the_image_with_the_commit_rather_than_latest():
    """A rollback has to be able to name an image. `:latest` is a guess about what it pointed at
    last Tuesday, and two pipelines racing on it is a deploy nobody can reconstruct."""
    for name in DEPLOY_WORKFLOWS:
        workflow = _workflow(name)
        steps = [line for line in workflow.splitlines() if not line.lstrip().startswith("#")]
        assert ":latest" not in "\n".join(steps), name
        assert "github.sha" in workflow, name


def test_no_deploy_workflow_carries_a_cookiecutter_variable():
    """`.github/workflows/` is `_copy_without_render`, because a GitHub Actions expression and a
    Jinja one are written with the same two braces. A cookiecutter variable in one of these files
    is therefore never substituted: it survives into the generated repository as literal text."""
    # The patterns are written with a character class around the second brace so that this file —
    # which *is* rendered — contains no Jinja delimiter of its own. Spelling them literally would
    # make the test an expression cookiecutter evaluates rather than text it copies.
    for name in WORKFLOWS:
        workflow = _workflow(name)
        assert not re.search(r"\{[{]\s*cookiecutter", workflow), name
        assert not re.search(r"\{[%]", workflow), name


def test_no_token_survives_into_an_installed_workflow():
    """The component's name and its name in capitals, as two tokens, are the only templating in a
    workflow, and the post-generation hook substitutes them. One left behind is a path filter that
    matches nothing or a variable nobody sets, and neither fails loudly."""
    tokens = ("__COMPONENT" + "__", "__COMPONENT" + "_VAR__")
    for name in WORKFLOWS:
        workflow = _workflow(name)
        for token in tokens:
            assert token not in workflow, (name, token)
    assert not (COMPONENT_DIR / ".github").exists(), "the hook moves the workflows to the root"


def _jobs(workflow: str) -> dict[str, str]:
    """Each job's body, by job id: the text from its two-space key to the next one."""
    body = workflow.split("\njobs:\n", 1)[1]
    parts = re.split(r"^  ([a-z][a-z0-9_-]*):\n", body, flags=re.M)
    return dict(zip(parts[1::2], parts[2::2], strict=True))


def test_every_workflow_is_named_and_scoped_for_its_component():
    """Two components' workflows sit side by side in one directory, run in one Actions tab and
    share one concurrency namespace. Every one says whose it is."""
    for name in WORKFLOWS:
        workflow = _workflow(name)
        assert re.search(rf"^name: {re.escape(COMPONENT)} ", workflow, re.M), name
        assert re.search(rf"^  group: {re.escape(COMPONENT)}-", workflow, re.M), name
        assert f"defaults:\n  run:\n    working-directory: {COMPONENT}\n" in workflow, name
        assert not re.search(r"vars\.(?!" + re.escape(VAR) + r"_)[A-Z]", workflow), name
        assert not re.search(
            r"secrets\.(?!" + re.escape(VAR) + r"_|GITHUB_TOKEN)[A-Z]", workflow
        ), name


def test_every_job_checks_out_before_anything_else():
    """A job whose working directory is the component's folder cannot run a step there before the
    checkout has put the folder on disk: "No such file or directory", before the step it guards."""
    for name in WORKFLOWS:
        for job, body in _jobs(_workflow(name)).items():
            steps = body.split("    steps:\n", 1)[1]
            first = re.split(r"^      - ", steps, flags=re.M)[1]
            assert first.startswith("uses: actions/checkout@"), (name, job, first.splitlines()[0])


def test_the_secret_scan_fails_on_a_finding_and_reads_the_components_config():
    """The gitleaks CLI from its release image — the action wants a paid licence on an
    organisation's repository — over the component's whole history, with its own config."""
    ci = _workflow("ci.yml")
    assert "uses: gitleaks/gitleaks-action" not in ci
    assert re.search(r"ghcr\.io/gitleaks/gitleaks:v[0-9]+\.[0-9]+\.[0-9]+", ci)
    assert f"--config {COMPONENT}/.gitleaks.toml" in ci
    assert f'--log-opts="-- {COMPONENT}"' in ci
    assert "--exit-code 0" not in ci and "continue-on-error" not in ci
    assert "fetch-depth: 0" in ci
{%- if cookiecutter.use_postgres == "yes" %}


# --- Database -------------------------------------------------------------


RUNTIME_EMAIL = f"{CONFIG.account_id('run')}@test-project.iam.gserviceaccount.com"


def _database() -> Any:
    from components import Database

    return Database(CONFIG, iam_service_account_email=RUNTIME_EMAIL)


@pulumi.runtime.test
def test_the_database_accepts_no_direct_connections():
    """An authorised network here would expose Postgres to the internet. With none, and connector
    enforcement on — GCP infrastructure standards, for a public-IP instance — there is no route in
    but a Cloud SQL connector, authenticated by IAM."""

    def check(settings):
        assert settings.ip_configuration.authorized_networks == []
        assert settings.ip_configuration.ssl_mode == "ENCRYPTED_ONLY"
        assert settings.connector_enforcement == "REQUIRED"
        return True

    return _database().instance.settings.apply(check)


@pulumi.runtime.test
def test_the_instance_accepts_iam_logins():
    """Without the flag the service's IAM user exists and every token it presents is refused."""

    def check(settings):
        flags = {(f.name, f.value) for f in settings.database_flags}
        assert ("cloudsql.iam_authentication", "on") in flags
        return True

    return _database().instance.settings.apply(check)


def test_the_service_logs_in_as_its_own_iam_user_with_no_password():
    """The user is the runtime service account, named as Cloud SQL names one: its email without
    `.gserviceaccount.com`. It carries no password, so there is nothing for state to hold."""
    MOCKS.resources.clear()
    holder: dict[str, Any] = {}
    pulumi.runtime.test(lambda: holder.setdefault("database", _database()))()
    (user,) = [r for r in MOCKS.resources if r.typ == "gcp:sql/user:User"]
    assert user.inputs["type"] == "CLOUD_IAM_SERVICE_ACCOUNT"
    assert user.inputs["name"] == RUNTIME_EMAIL.removesuffix(".gserviceaccount.com")
    assert "password" not in user.inputs
    assert _resolved(holder["database"].iam_user_name) == user.inputs["name"]


def test_the_role_is_dropped_after_the_database_and_not_beside_it():
    """The one ordering in this stack that only matters on the way out.

    Pulumi deletes a dependency graph backwards, so `database depends_on iam_user` is what makes
    the database go first and the role second. Both resources name the same instance, and that is
    not a dependency Pulumi can infer: `instance=` ties each of them to the *instance*, not to each
    other. Without the edge they are deleted concurrently, Cloud SQL drops the IAM user while the
    database still holds the privileges every migration granted it, and Postgres refuses —
    `role ... cannot be dropped because some objects depend on it`. The destroy stops there with
    the instance still running and still billing.

    Asserted through a transformation because `depends_on` is consumed at registration and kept on
    no attribute afterwards; there is nothing on the resource to read it back from. A
    transformation on the component reaches every child and sees the options as passed.
    """
    from components import Database

    seen: dict[str, Any] = {}

    def record(
        args: pulumi.ResourceTransformationArgs,
    ) -> pulumi.ResourceTransformationResult:
        seen[args.name] = args.opts.depends_on
        return pulumi.ResourceTransformationResult(props=args.props, opts=args.opts)

    database = Database(
        CONFIG,
        iam_service_account_email=RUNTIME_EMAIL,
        opts=pulumi.ResourceOptions(transformations=[record]),
    )
    depends_on = seen["database"]
    assert isinstance(depends_on, list) and database.iam_user in depends_on, (
        "sql.Database must depend on sql.User, or destroy deletes the role and the database "
        "at the same time and Cloud SQL refuses to drop the role"
    )


@pulumi.runtime.test
def test_the_database_uses_the_cheapest_honest_tier():
    def check(settings):
        assert settings.tier == "db-f1-micro"
        assert settings.availability_type == "ZONAL"
        assert settings.disk_size == 10
        return True

    return _database().instance.settings.apply(check)


@pulumi.runtime.test
def test_the_migration_job_exists_and_carries_the_database():
    """A migrate job left on the bootstrap image queues executions that never start, and nothing
    reports an error, because a job that cannot start is not a job that failed."""
    from components import JobSpec

    service = _service(
        secret_refs={"db-admin-password": SecretRef(secret="db-admin-password-dev", version="2")},
        jobs=[
            JobSpec(
                name="migrate",
                command=["alembic"],
                args=["upgrade", "head"],
                secret_env={"DATABASE_PASSWORD": "db-admin-password"},
                service_account="migrator@test.iam.gserviceaccount.com",
                cloud_sql_instance="test-project:europe-west1:app-pg-dev",
            )
        ],
    )
    assert "migrate" in service.jobs

    def check(template):
        inner = template.template
        assert inner.service_account == "migrator@test.iam.gserviceaccount.com"
        assert inner.volumes[0].cloud_sql_instance.instances == [
            "test-project:europe-west1:app-pg-dev"
        ]
        (container,) = inner.containers
        assert container.commands == ["alembic"]
        assert container.volume_mounts[0].mount_path == "/cloudsql"
        (password,) = container.envs
        assert password.value_source.secret_key_ref.version == "2"
        return True

    return service.jobs["migrate"].template.apply(check)


def _env(program: Program, name: str) -> dict[str, Any]:
    return {key: _resolved(value) for key, value in program.globals[name].items()}


def test_the_service_reaches_the_database_through_the_connector_as_itself():
    """No password, no DSN, no socket and no Cloud SQL volume on the service: the connector logs in
    as the runtime account's IAM user. The backend reads exactly these four settings."""
    program = _program(jwtSecretVersion="3", dbAdminPasswordVersion="2")
    env = _env(program, "BACKEND_ENV")
    assert env["DATABASE_AUTH"] == "cloud-sql-iam"
    assert env["CLOUD_SQL_INSTANCE"] == "test-project:europe-west1:app-pg-dev"
    assert env["DATABASE_NAME"] == "app"
    assert env["DATABASE_USER"] == f"{CONFIG.account_id('run')}@test.iam"
    assert not {"DATABASE_URL_OVERRIDE", "DATABASE_PASSWORD"} & set(env)
    assert program.globals["SECRET_ENV"]["JWT_SECRET"] == "jwt-secret"

    (service,) = program.of_type("gcp:cloudrunv2/service:Service")
    assert not service.inputs["template"].get("volumes"), "the service mounts no Cloud SQL socket"


def test_the_migration_job_runs_as_the_admin_login_and_grants_the_service_its_tables():
    """`password` mode over the Unix socket Cloud Run mounts, as `postgres`, with the password
    pinned to `dbAdminPasswordVersion` — and the service's IAM user named as the role every run
    grants the tables to."""
    program = _program(jwtSecretVersion="3", dbAdminPasswordVersion="2")
    env = _env(program, "MIGRATE_ENV")
    assert env["DATABASE_AUTH"] == "password"
    assert env["DATABASE_HOST"] == "/cloudsql/test-project:europe-west1:app-pg-dev"
    assert (env["DATABASE_NAME"], env["DATABASE_USER"]) == ("app", "postgres")
    assert env["DATABASE_APP_ROLE"] == f"{CONFIG.account_id('run')}@test.iam"
    assert env["DATABASE_APP_AUTH"] == "cloud-sql-iam", "checked for by the migration, not created"

    (job,) = program.of_type("gcp:cloudrunv2/job:Job")
    inner = job.inputs["template"]["template"]
    assert inner["serviceAccount"] == f"{CONFIG.account_id('migrate')}@test.iam.gserviceaccount.com"
    assert inner["volumes"][0]["cloudSqlInstance"]["instances"] == [
        "test-project:europe-west1:app-pg-dev"
    ]
    (password,) = [e for e in inner["containers"][0]["envs"] if "valueSource" in e]
    assert password["name"] == "DATABASE_PASSWORD"
    assert password["valueSource"]["secretKeyRef"]["version"] == "2"
{%- endif %}
{%- if cookiecutter.use_sentry == "yes" %}


# --- Manual secrets -------------------------------------------------------


@pulumi.runtime.test
def test_the_sentry_container_is_created_empty_and_mounted_by_nothing():
    """It holds a value that originates outside this stack, so the stack cannot create a version.
    Nothing may mount it until a human has added one and set `sentryDsnVersion`."""
    secrets = Secrets(
        CONFIG, accessors={"sentry-dsn": "runtime@test.iam.gserviceaccount.com"}, versions={}
    )
    assert "sentry-dsn" in secrets.ids
    assert "sentry-dsn" not in secrets.refs
    assert CONFIG.sentry_dsn_version == ""
    assert "SENTRY_DSN" not in _program().globals["SECRET_ENV"]
    return pulumi.Output.from_input(True)


@pulumi.runtime.test
def test_a_populated_sentry_secret_is_pinned_to_its_configured_version():
    """Once a human has added the DSN and recorded its number, that number is what is mounted."""
    secrets = Secrets(
        CONFIG,
        accessors={"sentry-dsn": "runtime@test.iam.gserviceaccount.com"},
        versions={"sentry-dsn": "3"},
    )
    assert secrets.refs["sentry-dsn"].version == "3"
    return pulumi.Output.from_input(True)
{%- endif %}
