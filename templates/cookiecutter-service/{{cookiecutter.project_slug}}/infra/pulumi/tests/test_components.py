"""Infrastructure unit tests. No cloud credentials, no network, no state.

Being able to assert on a stack without applying it is most of the argument for Pulumi over HCL, so
these run in the ordinary `pytest` invocation and on every pull request that touches `infra/`.

What is worth testing here is not "does Pulumi work" but the handful of settings where a silent
mistake is expensive: a database open to the internet, a Workload Identity pool any repository can
use, a service running as the default compute account, or a stack whose export names have drifted
from the variable names the deploy pipeline reads.
"""

from __future__ import annotations

import re
from pathlib import Path
from typing import Any

import pulumi
import pytest

HERE = Path(__file__).resolve().parent
PULUMI_DIR = HERE.parent
REPO = PULUMI_DIR.parent.parent


class Mocks(pulumi.runtime.Mocks):
    def new_resource(self, args: pulumi.runtime.MockResourceArgs) -> tuple[str, dict[str, Any]]:
        outputs = dict(args.inputs)
        if args.typ == "gcp:sql/databaseInstance:DatabaseInstance":
            outputs["connectionName"] = "test-project:europe-west1:app-pg-dev"
        if args.typ == "gcp:serviceaccount/account:Account":
            outputs["email"] = f"{args.inputs.get('accountId', 'sa')}@test.iam.gserviceaccount.com"
        return f"{args.name}-id", outputs

    def call(
        self, args: pulumi.runtime.MockCallArgs
    ) -> tuple[dict[str, Any], list[tuple[str, str]] | None]:
        if args.token == "gcp:organizations/getProject:getProject":
            return {"number": "123456789", "projectId": "test-project"}, None
        return {}, None


pulumi.runtime.set_mocks(Mocks(), preview=False)

from components import Identities, Secrets, Service  # noqa: E402 - must follow set_mocks
from config import StackConfig  # noqa: E402 - must follow set_mocks

SLUG = "{{ cookiecutter.project_slug }}"

CONFIG = StackConfig(
    project="test-project",
    region="europe-west1",
    environment="dev",
    github_repository="{{ cookiecutter.github_repository }}",
    slug=SLUG,
)


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
    import dataclasses

    with pytest.raises(ValueError, match="appEnvironment"):
        dataclasses.replace(CONFIG, app_environment="dev")


# --- Naming ---------------------------------------------------------------


def test_service_account_ids_fit_what_gcp_accepts():
    """A service-account id is 6-30 characters, and a long project name is the way past that.

    The failure without this is a mid-apply rejection on a name nobody looked at, after the APIs
    and the registry have already been created.
    """
    for account in (CONFIG.account_id("run"), CONFIG.account_id("ci")):
        assert 6 <= len(account) <= 30, account
        assert re.fullmatch(r"[a-z][a-z0-9-]{4,28}[a-z0-9]", account), account


def test_a_long_project_name_is_shortened_rather_than_rejected():
    import dataclasses

    long = dataclasses.replace(CONFIG, slug="a-very-long-service-name-indeed", environment="prod")
    assert len(long.account_id("run")) <= 30
    assert long.account_id("run") != long.account_id("ci")


def test_the_pool_id_is_unique_within_the_project_not_just_the_stack():
    """Two services in one GCP project must not both want the same pool.

    A pool id is project-global. Building it from the kind and the environment alone — `github-dev`
    for everybody — means the second service generated from this template fails `pulumi up` with a
    409 on a pool the first one owns, and the id cannot be freed for 30 days.
    """
    import dataclasses

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
    import dataclasses

    api = dataclasses.replace(CONFIG, slug="platform-billing-service-api")
    web = dataclasses.replace(CONFIG, slug="platform-billing-service-web")
    assert api.pool_id("github") != web.pool_id("github")


def test_the_same_slug_in_two_repositories_is_two_pools():
    """Two teams both calling their service `orders` in one project must not fight over a name."""
    import dataclasses

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
    import dataclasses

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


@pulumi.runtime.test
def test_the_service_runs_as_the_runtime_account_not_the_default():
    """The default compute account holds Editor on the whole project."""
    return _service().service.template.apply(
        lambda template: template.service_account == "runtime@test.iam.gserviceaccount.com"
    )


def test_a_service_refuses_a_secret_it_was_never_given():
    """Otherwise the failure is a revision that cannot resolve `secret:latest`, which reads as a
    broken application rather than a mistyped key."""
    with pytest.raises(KeyError, match="typo"):
        Service(
            CONFIG,
            app=SLUG,
            runtime_email="runtime@test.iam.gserviceaccount.com",
            port=8080,
            health_path="/health/live",
            secret_env={"DATABASE_URL_OVERRIDE": "typo"},
            secret_ids={"database-url": pulumi.Output.from_input("x")},
        )


@pulumi.runtime.test
def test_every_container_without_a_version_is_declared_manual():
    """Cloud Run resolves `secret:latest` when a revision is *created*.

    A container with no versions is therefore not "configured later" — it is a deploy that fails
    outright. So a secret the stack can generate gets its version here, and a secret that
    genuinely comes from outside is listed in `MANUAL_SECRETS` and must not be mounted until a
    human has populated it.
    """
    from components.secrets import MANUAL_SECRETS

    secrets = Secrets(
        CONFIG,
        accessor_email="runtime@test.iam.gserviceaccount.com",
{%- if cookiecutter.use_postgres == "yes" %}
        database_url="postgresql+asyncpg://app:pw@/app?host=/cloudsql/x",
        jwt_secret="not-the-real-one-the-stack-generates-it",
{%- endif %}
    )
    generated = set(secrets.ids) - set(MANUAL_SECRETS)
    assert len(secrets.versions) == len(generated)
{%- if cookiecutter.use_postgres == "yes" %}
    # The signing key in particular: a service that cannot resolve it does not start, so
    # "populate it later" is not an option the way it is for a Sentry DSN.
    assert "jwt-secret" in generated
{%- endif %}
    return pulumi.Output.from_input(True)


def test_no_secret_value_is_written_into_the_stack_source():
    """Secret values never appear in infrastructure code. The one `secret_data=` is a parameter,
    and what reaches it — the database DSN, the signing key — is generated, never typed."""
    for source in (PULUMI_DIR / "components").glob("*.py"):
        for line in source.read_text().splitlines():
            if "secret_data=" in line:
                assert "value" in line, f"{source.name}: {line.strip()}"
{%- if cookiecutter.use_postgres == "yes" %}


def test_the_migration_job_is_not_granted_the_signing_key():
    """It signs nothing. A job holding a credential it never uses is a credential with a second
    place to leak from, and this one is the whole of the auth system."""
    program = (PULUMI_DIR / "__main__.py").read_text()
    assert 'SECRET_ENV["JWT_SECRET"] = "jwt-secret"' in program
    job = program.partition("JobSpec(")[2].partition(")")[0]
    assert "jwt-secret" not in job, job
{%- endif %}


# --- Cloud Run shape ------------------------------------------------------


def _service(**overrides: Any) -> Service:
    kwargs: dict[str, Any] = {
        "app": SLUG,
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
def test_a_service_without_a_database_mounts_nothing():
    """An unused Cloud SQL attachment is a permission the service does not need."""

    def check(template):
        assert template.volumes == []
        assert template.containers[0].volume_mounts == []
        return True

    return _service().service.template.apply(check)


def test_the_pipeline_owns_the_image_and_nothing_else_does():
    """Two tools writing one field is how an infrastructure repository stops being trusted."""
    source = (PULUMI_DIR / "components" / "service.py").read_text()
    assert 'ignore_changes=["template.containers[0].image"' in source
    assert '"template.template.containers[0].image"' in source


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
    assert "! -t 0" in script, "must refuse when nothing can confirm"
    assert '"${reply}" != "${BUCKET}"' in script, "must make a human type the bucket name"


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
    "SENTRY_ORG",
    "SENTRY_PROJECT",
{%- if cookiecutter.include_frontend == "yes" %}
    # Only needed when the API answers on a domain you own rather than on its Cloud Run URL.
    # Unset, the frontend build falls back to SERVICE_URL, which this stack does export.
    "FRONTEND_API_URL",
{%- endif %}
}

#: Every workflow that deploys something. Each one is a fresh chance for a variable name to drift
#: from the stack that is supposed to supply it, so each one is checked.
{%- if cookiecutter.include_frontend == "yes" %}
DEPLOY_WORKFLOWS = (
    "cd.yml",
    "frontend-cd.yml",
)
{%- else %}
DEPLOY_WORKFLOWS = ("cd.yml",)
{%- endif %}


def _workflow(name: str) -> str:
    return (REPO / ".github" / "workflows" / name).read_text()


def _sync_script() -> str:
    return (PULUMI_DIR.parent / "scripts" / "sync-github.sh").read_text()


def test_every_variable_a_deploy_reads_is_one_the_sync_script_sets():
    provided = set(re.findall(r"gh variable set \"?([A-Z0-9_]+)", _sync_script()))
    provided |= set(re.findall(r'"[a-z_]+:([A-Z0-9_]+)"', _sync_script()))
    for name in DEPLOY_WORKFLOWS:
        referenced = set(re.findall(r"vars\.([A-Z0-9_]+)", _workflow(name))) - MANUAL_VARIABLES
        assert referenced <= provided, f"{name}: never set: {sorted(referenced - provided)}"


def test_every_output_the_sync_script_reads_is_one_the_stack_exports():
    mapped = set(re.findall(r'"([a-z_]+):[A-Z0-9_]+"', _sync_script()))
    program = (PULUMI_DIR / "__main__.py").read_text()
    exported = set(re.findall(r'pulumi\.export\(\s*"([a-z_]+)"', program))
    assert mapped <= exported, f"never exported: {sorted(mapped - exported)}"


def test_every_deploy_stays_inert_until_it_is_configured():
    """A committed deploy path that cannot fire by accident is reviewed alongside the code it
    ships, rather than written under pressure on the day."""
    for name in DEPLOY_WORKFLOWS:
        assert "if: vars.WIF_PROVIDER != ''" in _workflow(name), name


def test_every_deploy_is_filtered_to_the_component_it_deploys():
    """Without a filter, a typo fix in a README rebuilds and redeploys a container."""
    for name in DEPLOY_WORKFLOWS:
        assert "paths:" in _workflow(name), name


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
    for name in DEPLOY_WORKFLOWS:
        workflow = _workflow(name)
        assert not re.search(r"\{[{]\s*cookiecutter", workflow), name
        assert not re.search(r"\{[%]", workflow), name
{%- if cookiecutter.use_postgres == "yes" %}


# --- Database -------------------------------------------------------------


@pulumi.runtime.test
def test_the_database_accepts_no_direct_connections():
    """An authorised network here would expose Postgres to the internet. With none, there is no
    route in at all: Cloud Run reaches it over the connector's Unix socket, authenticated by IAM."""
    from components import Database

    def check(settings):
        assert settings.ip_configuration.authorized_networks == []
        assert settings.ip_configuration.ssl_mode == "ENCRYPTED_ONLY"
        return True

    return Database(CONFIG).instance.settings.apply(check)


@pulumi.runtime.test
def test_the_database_uses_the_cheapest_honest_tier():
    from components import Database

    def check(settings):
        assert settings.tier == "db-f1-micro"
        assert settings.availability_type == "ZONAL"
        assert settings.disk_size == 10
        return True

    return Database(CONFIG).instance.settings.apply(check)


@pulumi.runtime.test
def test_the_migration_job_exists_and_carries_the_database():
    """A migrate job left on the bootstrap image queues executions that never start, and nothing
    reports an error, because a job that cannot start is not a job that failed."""
    from components import JobSpec

    service = _service(
        connection_name="test-project:europe-west1:app-pg-dev",
        secret_ids={"database-url": pulumi.Output.from_input("database-url-dev")},
        jobs=[
            JobSpec(
                name="migrate",
                command=["alembic"],
                args=["upgrade", "head"],
                secret_env={"DATABASE_URL_OVERRIDE": "database-url"},
            )
        ],
    )
    assert "migrate" in service.jobs

    def check(template):
        containers = template.template.containers
        assert containers is not None
        assert containers[0].commands == ["alembic"]
        return True

    return service.jobs["migrate"].template.apply(check)


@pulumi.runtime.test
def test_the_database_dsn_is_generated_rather_than_typed():
    """A password a human chose is a password a human can reuse. This one only ever travels from
    Secret Manager into the runtime environment, and nobody ever sees it."""
    secrets = Secrets(
        CONFIG,
        accessor_email="runtime@test.iam.gserviceaccount.com",
        database_url="postgresql+asyncpg://app:pw@/app?host=/cloudsql/x",
    )
    assert "database-url" in secrets.ids
    assert len(secrets.versions) == 1
    return pulumi.Output.from_input(True)
{%- endif %}
{%- if cookiecutter.use_sentry == "yes" %}


# --- Manual secrets -------------------------------------------------------


@pulumi.runtime.test
def test_the_sentry_container_is_created_empty_and_mounted_by_nothing():
    """It holds a value that originates outside this stack, so the stack cannot create a version.
    Nothing may mount it until a human has added one — that is what `sentryDsnSet` gates."""
    from components.secrets import MANUAL_SECRETS

    assert "sentry-dsn" in MANUAL_SECRETS
    secrets = Secrets(CONFIG, accessor_email="runtime@test.iam.gserviceaccount.com")
    assert "sentry-dsn" in secrets.ids
    assert secrets.versions == []
    assert CONFIG.sentry_dsn_set is False
    return pulumi.Output.from_input(True)
{%- endif %}
{%- if cookiecutter.include_frontend == "yes" %}


# --- The frontend ---------------------------------------------------------
#
# The failure this section exists to prevent: a generated repository that has a React application,
# a CI pipeline that builds it, and nothing anywhere that deploys it.


def _program() -> str:
    return (PULUMI_DIR / "__main__.py").read_text()


def test_the_frontend_has_its_own_runtime_account():
    """Sharing the backend's account would hand a public static web server — the least protected
    surface here, with no application code in it — the backend's database role and whatever secret
    grants it collects later."""
    from components.identities import RUNTIME_ROLES, WEB_RUNTIME_ROLES

    identities = Identities(CONFIG, project_number="123456789")
    assert identities.web_runtime is not identities.runtime
    assert CONFIG.account_id("web") != CONFIG.account_id("run")
    for role in WEB_RUNTIME_ROLES:
        assert "sql" not in role and "secret" not in role, role
    # Never more than the service it sits in front of.
    assert set(WEB_RUNTIME_ROLES) <= set(RUNTIME_ROLES)


@pulumi.runtime.test
def test_the_frontend_probes_a_path_nginx_actually_serves():
    """`/health/live` is a backend route and a static bundle does not have one. A startup probe
    pointed at it fails forever, and the first revision never takes traffic."""
    frontend = Service(
        CONFIG,
        app=f"{SLUG}-web",
        runtime_email="web-runtime@test.iam.gserviceaccount.com",
        port=8080,
        health_path="/",
    )

    def check(template):
        container = template.containers[0]
        assert container.startup_probe.http_get.path == "/"
        assert container.startup_probe.http_get.port == 8080
        # No database, no secret, no environment: this container serves files off its own disk,
        # and every value the bundle needed was inlined before the image existed.
        assert template.volumes == []
        assert container.volume_mounts == []
        assert container.envs == []
        return True

    return frontend.service.template.apply(check)


def test_the_composition_root_declares_a_service_for_the_frontend():
    program = _program()
    assert 'app=f"{config.slug}-web"' in program
    assert "runtime_email=identities.web_runtime.email" in program
    assert 'health_path="/"' in program
    assert 'pulumi.export("cloud_run_frontend_service"' in program


def test_the_backend_allows_the_deployed_frontend_origin():
    """Otherwise the SPA loads, makes exactly one request, and the browser refuses the answer —
    which reads as an API outage rather than as a missing allowlist entry. The origin is a Cloud
    Run URL, so it cannot be typed in ahead of time; it has to be wired from the stack."""
    assert 'BACKEND_ENV["CORS_ORIGINS"] = frontend.url' in _program()


def test_the_frontend_bundle_is_compiled_against_an_origin_the_stack_supplies():
    """Vite inlines VITE_API_URL at BUILD time, so the API origin is baked into the JavaScript and
    no amount of Cloud Run configuration changes it afterwards. A build that runs before the
    backend has a URL produces a bundle calling localhost that deploys, serves and answers
    nothing — so the pipeline fails the run instead of shipping it."""
    workflow = _workflow("frontend-cd.yml")
    assert "VITE_API_URL=" in workflow
    assert "vars.SERVICE_URL" in workflow
    assert "::error::" in workflow
{%- endif %}
