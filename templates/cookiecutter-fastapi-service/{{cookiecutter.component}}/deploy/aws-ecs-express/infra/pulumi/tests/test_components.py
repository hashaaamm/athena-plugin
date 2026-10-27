"""Component and configuration tests. No cloud credentials, no network, no state.

Being able to assert on a stack without applying it is most of the argument for Pulumi over HCL, so
these run in the ordinary `pytest` invocation and on every pull request that touches `infra/`.

What is worth testing here is not "does Pulumi work" but the handful of settings where a silent
mistake is expensive: a CPU and memory pair Fargate refuses mid-apply, a secret mounted at whatever
its current value happens to be, a service whose image the next apply rolls back, or a teardown
script that deletes a bucket somebody else's state lives in. The whole program, run end to end
against the same mocks, is in `test_stack.py`.
"""

from __future__ import annotations

import dataclasses
import re
from collections.abc import Callable
from functools import partial
from pathlib import Path
from typing import Any

import pulumi
import pytest

from tests.mocks import ACCOUNT, IMAGE, MOCKS, REGION, SLUG, Registered

# Importing tests.mocks installed the mocks. Every component import has to come after it.
MOCKS.reset()

from components import Logs, Registry, Secrets, Service  # noqa: E402 - after set_mocks
from components.secrets import SecretRef  # noqa: E402 - after set_mocks
from components.service import IMAGE_PATH  # noqa: E402 - after set_mocks
from config import StackConfig  # noqa: E402 - after set_mocks

PULUMI_DIR = Path(__file__).resolve().parent.parent
INFRA = PULUMI_DIR.parent

CONFIG = StackConfig(
    account_id=ACCOUNT,
    region=REGION,
    environment="dev",
    github_repository="{{ cookiecutter.github_repository }}",
    slug=SLUG,
    pulumi_project=SLUG,
)
PROD = dataclasses.replace(CONFIG, environment="prod")

EXPRESS = "aws:ecs/expressGatewayService:ExpressGatewayService"


def build(construct: Callable[[], Any]) -> list[Registered]:
    """Run `construct` against the mocks and return every resource it asked AWS for."""
    MOCKS.reset()
    pulumi.runtime.test(construct)()
    return list(MOCKS.resources)


def only(resources: list[Registered], typ: str) -> Registered:
    matches = [r for r in resources if r.typ == typ]
    assert len(matches) == 1, f"{typ}: {len(matches)} registered"
    return matches[0]


# --- The two environments -------------------------------------------------
#
# `environment` names resources and `app_environment` is what the application is told it is. They
# are read as one thing constantly, and the failure is quiet: a `dev` stack deploying a service
# that believes it is production serves no /docs and says nothing about why.


def test_the_stack_environment_names_resources_and_the_app_never_sees_it():
    """Two environments must be able to share one account without colliding."""
    assert CONFIG.name("api") == "api-dev"
    assert CONFIG.name(SLUG, "migrate").endswith("-migrate-dev")


def test_the_application_environment_is_configured_rather_than_hard_coded():
    """A literal here is a stack whose name says `dev` and whose service believes it is prod."""
    program = (PULUMI_DIR / "stack.py").read_text()
    assert '"ENVIRONMENT": config.app_environment' in program
    assert CONFIG.app_environment == "staging", "a first deploy should still answer /docs"


def test_an_application_environment_the_app_cannot_parse_is_refused():
    """Caught here, or several minutes into an apply by a container that exits on startup."""
    with pytest.raises(ValueError, match="appEnvironment"):
        dataclasses.replace(CONFIG, app_environment="dev")


# --- Configuration that AWS would refuse mid-apply ------------------------


@pytest.mark.parametrize(
    ("field", "value", "message"),
    [
        ("account_id", "12345", "accountId"),
        ("account_id", "12345678901a", "accountId"),
        ("region", "Ireland", "region"),
        ("github_repository", "just-a-name", "githubRepository"),
        ("github_repository", "owner/*", "githubRepository"),
        ("github_subject_prefix", "acme/billing-api", "githubSubjectPrefix"),
        ("github_subject_prefix", "repo:acme@*/billing-api@*", "githubSubjectPrefix"),
        ("github_subject_prefix", "repo:acme/x:", "githubSubjectPrefix"),
        ("min_tasks", 0, "scale to zero"),
        ("max_tasks", 0, "maxTasks"),
        ("availability_zone_count", 1, "availabilityZoneCount"),
        ("availability_zone_count", 4, "availabilityZoneCount"),
        ("cpu", "300", "cpu"),
        ("memory", "4096", "memory"),
        ("slug", "Has_Caps", "slug"),
{%- if cookiecutter.use_postgres == "yes" %}
        # Named in an IAM policy and created by the migration; quoting is not on offer in either.
        ("database_app_role", "App-IAM", "databaseAppRole"),
        ("database_app_role", "app iam", "databaseAppRole"),
        ("database_app_role", "a" * 64, "databaseAppRole"),
        # The master user owns the schema and has a password; the service must hold neither.
        ("database_app_role", "app", "master user"),
{%- endif %}
    ],
)
def test_configuration_aws_would_refuse_is_refused_when_the_stack_loads(field, value, message):
    """Every one of these is otherwise an error from AWS with half the stack already created."""
    with pytest.raises(ValueError, match=message):
        dataclasses.replace(CONFIG, **{field: value})


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
def test_a_cors_entry_that_is_not_an_origin_is_refused(origin):
    """The application refuses the same entries and exits on start-up. A browser never sends a
    path or a trailing slash in `Origin`, so such an entry is dead configuration that looks
    alive."""
    with pytest.raises(ValueError, match="corsOrigins entry"):
        dataclasses.replace(CONFIG, cors_origins=(origin,))


def test_cors_origins_are_parsed_from_a_comma_separated_list():
    from config import parse_origins

    assert parse_origins("") == ()
    assert parse_origins(" https://app.example.com , ,http://localhost:3000 ") == (
        "https://app.example.com",
        "http://localhost:3000",
    )
    dataclasses.replace(CONFIG, cors_origins=parse_origins("http://localhost:3000"))


def test_browser_settings_default_to_no_browser():
{%- if cookiecutter.use_postgres == "yes" %}
    assert CONFIG.browser_env == {"REFRESH_TRANSPORT": "body"}
{%- else %}
    assert CONFIG.browser_env == {}
{%- endif %}
{%- if cookiecutter.use_postgres == "yes" %}


def test_a_transport_the_app_cannot_parse_is_refused():
    with pytest.raises(ValueError, match="refreshTransport"):
        dataclasses.replace(CONFIG, refresh_transport="header")


def test_a_cookie_transport_without_an_origin_is_refused():
    """The cookie routes' CSRF check accepts only listed origins, so the application refuses to
    start with this pair. Refused here, at preview, rather than by tasks that never get healthy."""
    with pytest.raises(ValueError, match="requires corsOrigins"):
        dataclasses.replace(CONFIG, refresh_transport="cookie")
    dataclasses.replace(
        CONFIG, refresh_transport="cookie", cors_origins=("https://app.example.com",)
    )
{%- endif %}


def test_every_fargate_size_the_config_accepts_is_one_fargate_offers():
    """256 CPU units with 4 GB is a task definition ECS rejects; 512 with 512 MiB is too."""
    dataclasses.replace(CONFIG, cpu="256", memory="2048")
    dataclasses.replace(CONFIG, cpu="1024", memory="8192")
    with pytest.raises(ValueError, match="memory"):
        dataclasses.replace(CONFIG, cpu="512", memory="512")


def _stack_file() -> dict[str, str]:
    """`Pulumi.dev.yaml`'s configuration, as the strings Pulumi hands the program."""
    lines = (PULUMI_DIR / "Pulumi.dev.yaml").read_text().splitlines()
    pairs = (re.fullmatch(r"\s+([a-z0-9-]+:\w+): (.*)", line) for line in lines)
    return {m.group(1): m.group(2).strip('"') for m in pairs if m}


def test_the_stack_file_loads_into_the_configuration_the_program_runs_with():
    """A key spelled one way in `Pulumi.dev.yaml` and another in `config.py` is not an error: the
    program quietly runs on the default. So every key the file sets is read, and reads as itself."""
    from config import load

    document = _stack_file()
    assert all(key.startswith(f"{SLUG}:") for key in document), document
    read = set(re.findall(r'config\.(?:require|get|get_int|get_bool)\("(\w+)"\)', _config_source()))
    assert {key.split(":", 1)[1] for key in document} <= read

    pulumi.runtime.set_all_config(document)
    try:
        loaded = load()
    finally:
        pulumi.runtime.set_all_config({})
    assert loaded.account_id == document[f"{SLUG}:accountId"]
    assert loaded.slug == SLUG
    assert loaded.pulumi_project == SLUG, "the preview's state grant is scoped by this name"
    assert (loaded.cpu, loaded.memory) == ("256", "512")
    assert loaded.service_image == "", "set by `just infra-first-deploy`, never by hand"
    assert not loaded.has_service
    assert loaded.cors_origins == (), "no browser may call a fresh stack"
{%- if cookiecutter.use_postgres == "yes" %}
    assert loaded.refresh_transport == "body"
    assert loaded.database_app_role == "app_iam"
    assert loaded.jwt_secret_version == "", "pinned by `just infra-secrets`, never by hand"
    assert not loaded.secrets_ready
{%- endif %}
{%- if cookiecutter.use_postgres == "yes" %}
    assert loaded.database_storage_gb == 20
{%- endif %}


def _config_source() -> str:
    return (PULUMI_DIR / "config.py").read_text()


def test_an_account_id_with_a_leading_zero_survives_the_stack_file():
    """YAML reads an unquoted 012345678901 as a shorter number. The stack file quotes it."""
    stack_file = (PULUMI_DIR / "Pulumi.dev.yaml").read_text()
    assert re.search(r':accountId: "[0-9]{12}"', stack_file), "accountId must be quoted"
    dataclasses.replace(CONFIG, account_id="012345678901")


# --- Naming ---------------------------------------------------------------


def test_role_names_fit_what_iam_accepts():
    """An IAM role name is at most 64 characters, and a long project name is the way past that."""
    long = dataclasses.replace(
        CONFIG, slug="a-" + "very-" * 14 + "long-service", environment="prod"
    )
    for role in (
        "infrastructure",
        "execution",
        "runtime",
        "migrate-exec",
        "deploy",
        "preview",
        "apply",
    ):
        for config in (CONFIG, long):
            name = config.role_name(role)
            assert len(name) <= 64, name
            assert name.endswith(f"-{role}-{config.environment}"), name


def test_two_long_slugs_sharing_a_prefix_do_not_trim_into_one_role():
    """IAM role names are account-global, so the second service to apply would get a 409.

    The digest is taken over the untrimmed slug, so what the trim discards still reaches the name.
    """
    stem = "platform-billing-service-with-a-name-long-enough-to-trim"
    api = dataclasses.replace(CONFIG, slug=f"{stem}-api")
    web = dataclasses.replace(CONFIG, slug=f"{stem}-web")
    assert api.role_name("deploy") != web.role_name("deploy")
    assert api.role_name("deploy") == api.role_name("deploy"), "a name has to be computable twice"


def test_a_short_slug_is_not_decorated():
    """The digest is for names that had to be trimmed; a name that fits reads as itself."""
    assert CONFIG.role_name("deploy") == f"{SLUG}-deploy-dev"


def test_arns_are_built_in_the_regions_partition():
    """`rds-db:connect` names the database user by ARN, and a GovCloud ARN is `aws-us-gov`."""
    assert CONFIG.partition == "aws"
    assert dataclasses.replace(CONFIG, region="us-gov-west-1").partition == "aws-us-gov"
{%- if cookiecutter.use_postgres == "yes" %}


def test_the_database_identifier_is_one_rds_accepts():
    """1-63 characters, starting with a letter. A slug may start with a digit; RDS may not."""
    for slug in (SLUG, "3d-renderer", "a-" + "very-" * 14 + "long-service"):
        identifier = dataclasses.replace(CONFIG, slug=slug).db_identifier()
        assert re.fullmatch(r"[a-z][a-z0-9-]{0,62}", identifier), identifier
        assert "--" not in identifier and not identifier.endswith("-"), identifier
{%- endif %}


# --- Secrets --------------------------------------------------------------


def test_a_secret_reference_pins_its_version_and_never_the_current_value():
    """A bare ARN means AWSCURRENT, read when a task *starts*: after a rotation the tasks of one
    deployment hold different values. ADR-0016's rule, in the shape ECS takes it."""
    ref = SecretRef(arn="arn:aws:secretsmanager:r:1:secret:s-AbCdEf", version_id="v-1")
    seen: list[str] = []

    def construct() -> pulumi.Output[None]:
        return ref.value_from().apply(seen.append)

    build(construct)
    assert seen == ["arn:aws:secretsmanager:r:1:secret:s-AbCdEf:::v-1"]


def test_secrets_are_deleted_at_once_outside_production_and_recoverable_in_it():
    """A secret pending deletion keeps its name, so a dev destroy and re-create would collide."""
    for config, window in ((CONFIG, 0), (PROD, 30)):
        resources = build(partial(Secrets, config, names=["jwt-secret"]))
        secrets = [r for r in resources if r.typ == "aws:secretsmanager/secret:Secret"]
        assert config.name(SLUG, "jwt-secret") in {s.inputs["name"] for s in secrets}
        assert {s.inputs["recoveryWindowInDays"] for s in secrets} == {window}


def test_every_secret_is_created_empty_and_referenced_only_once_pinned():
    """Pulumi standards MUST-3, ADR-0023: the stack creates the secret; the value is added out of
    band. A reference to a secret with no value is a task that cannot start, so nothing is
    referenced until its `VersionId` is configured."""
    holder: dict[str, Secrets] = {}
    resources = build(
        lambda: holder.setdefault("secrets", Secrets(CONFIG, names=["jwt-secret", "sentry-dsn"]))
    )
    secrets = holder["secrets"]
    assert set(secrets.arns) == {"jwt-secret", "sentry-dsn"}
    assert not [r for r in resources if r.typ == "aws:secretsmanager/secretVersion:SecretVersion"]
    assert secrets.refs == {}


def test_a_secret_is_pinned_to_its_configured_version_id():
    version = "3f5c8a0e-1b2d-4c3e-9f8a-7b6c5d4e3f2a"
    holder: dict[str, Secrets] = {}
    build(
        lambda: holder.setdefault(
            "secrets", Secrets(CONFIG, names=["jwt-secret"], versions={"jwt-secret": version})
        )
    )
    assert holder["secrets"].refs["jwt-secret"].version_id == version


def test_no_secret_value_is_in_the_program_or_its_dependencies():
    """Pulumi standards MUST-3. Nothing here generates, holds or writes a secret value: no secret
    version, no random password, no database password. What reaches state is a secret's ARN and a
    version id."""
    sources = [PULUMI_DIR / "stack.py", *(PULUMI_DIR / "components").glob("*.py")]
    for source in sources:
        text = source.read_text()
        for forbidden in ("SecretVersion(", "secret_string", "pulumi_random", "RandomPassword"):
            assert forbidden not in text, f"{source.name}: {forbidden}"
        assert not re.search(r"\bpassword\s*=", text), source.name
    assert "pulumi-random" not in (PULUMI_DIR / "pyproject.toml").read_text()
{%- if cookiecutter.use_sentry == "yes" %}


def test_the_sentry_secret_is_created_empty_and_referenced_by_nothing():
    """It holds a value that originates outside this stack, so the stack cannot create a version.
    Nothing may reference it until a human has added one and set `sentryDsnVersion`."""
    holder: dict[str, Secrets] = {}
    resources = build(lambda: holder.setdefault("secrets", Secrets(CONFIG, names=["sentry-dsn"])))
    secrets = holder["secrets"]
    assert "sentry-dsn" in secrets.arns
    assert not [r for r in resources if r.typ == "aws:secretsmanager/secretVersion:SecretVersion"]
    assert "sentry-dsn" not in secrets.refs
    assert CONFIG.sentry_dsn_version == ""


def test_a_populated_sentry_secret_is_pinned_to_its_configured_version():
    """Once a human has added the DSN and recorded its VersionId, that id is what is referenced."""
    version = "3f5c8a0e-1b2d-4c3e-9f8a-7b6c5d4e3f2a"
    holder: dict[str, Secrets] = {}
    build(
        lambda: holder.setdefault(
            "secrets", Secrets(CONFIG, names=["sentry-dsn"], versions={"sentry-dsn": version})
        )
    )
    assert holder["secrets"].refs["sentry-dsn"].version_id == version
{%- endif %}


# --- The Express service --------------------------------------------------


def _service(**overrides: Any) -> Service:
    kwargs: dict[str, Any] = {
        "app": SLUG,
        "image": IMAGE,
        "cluster": f"{SLUG}-dev",
        "execution_role_arn": f"arn:aws:iam::{ACCOUNT}:role/{SLUG}-execution-dev",
        "infrastructure_role_arn": f"arn:aws:iam::{ACCOUNT}:role/{SLUG}-infrastructure-dev",
        "task_role_arn": f"arn:aws:iam::{ACCOUNT}:role/{SLUG}-runtime-dev",
        "subnet_ids": ["subnet-a", "subnet-b"],
        "security_group_ids": ["sg-app"],
        "log_group": f"/ecs/{SLUG}-dev",
        "port": 8080,
        "health_path": "/health/live",
        "env": {"ENVIRONMENT": "production"},
    }
    kwargs.update(overrides)
    return Service(CONFIG, **kwargs)


def test_a_service_refuses_a_secret_it_was_never_given():
    """Otherwise the failure is a task that cannot resolve its secret, which reads as a broken
    application rather than a mistyped key."""
    with pytest.raises(KeyError, match="typo"):
        _service(
            secret_env={"JWT_SECRET": "typo"},
            secret_refs={"jwt-secret": SecretRef(arn="x", version_id="1")},
        )


def test_the_health_check_reads_liveness_on_the_container_port():
    """A health check pointed at readiness never lets a first deployment go healthy, because
    readiness can legitimately be false until something else has run."""
    service = only(build(_service), EXPRESS)
    assert service.inputs["healthCheckPath"] == "/health/live"
    assert service.inputs["primaryContainer"]["containerPort"] == 8080
    assert service.inputs["primaryContainer"]["image"] == IMAGE, "the real build, never a stand-in"
    assert "PORT" not in {e["name"] for e in service.inputs["primaryContainer"]["environments"]}


def test_the_service_runs_as_the_runtime_role_and_ecs_acts_as_the_execution_role():
    """The execution role reads secrets on ECS's behalf. Handing it to the application as well
    would let any code in the container read every secret the stack has."""
    service = only(build(_service), EXPRESS)
    assert service.inputs["taskRoleArn"].endswith("-runtime-dev")
    assert service.inputs["executionRoleArn"].endswith("-execution-dev")
    assert service.inputs["taskRoleArn"] != service.inputs["executionRoleArn"]


def test_the_service_writes_to_the_log_group_the_stack_owns():
    """Express Mode's default group keeps every line forever and outlives the service."""
    service = only(build(_service), EXPRESS)
    logs = service.inputs["primaryContainer"]["awsLogsConfigurations"]
    assert logs == [{"logGroup": f"/ecs/{SLUG}-dev", "logStreamPrefix": "ecs"}]


def test_the_service_scales_on_cpu_from_a_floor_of_one_task():
    service = only(build(_service), EXPRESS)
    assert service.inputs["scalingTargets"] == [
        {
            "minTaskCount": 1,
            "maxTaskCount": 4,
            "autoScalingMetric": "AVERAGE_CPU",
            "autoScalingTargetValue": 60,
        }
    ]
    assert service.inputs["waitForSteadyState"] is False


def test_a_referenced_secret_is_pinned_to_a_version_id():
    service = only(
        build(
            lambda: _service(
                secret_env={"JWT_SECRET": "jwt-secret"},
                secret_refs={"jwt-secret": SecretRef(arn="arn:secret", version_id="v-7")},
            )
        ),
        EXPRESS,
    )
    assert service.inputs["primaryContainer"]["secrets"] == [
        {"name": "JWT_SECRET", "valueFrom": "arn:secret:::v-7"}
    ]


def test_the_pipeline_owns_the_image_and_nothing_else_does():
    """Two tools writing one field is how an infrastructure repository stops being trusted. And the
    service waits for what it was told to wait for — the infrastructure role's policy above all,
    whose early deletion leaves a destroyed service stuck in DRAINING."""
    seen: dict[str, Any] = {}

    def record(args: pulumi.ResourceTransformationArgs) -> pulumi.ResourceTransformationResult:
        seen[args.type_] = args.opts
        return pulumi.ResourceTransformationResult(props=args.props, opts=args.opts)

    def construct() -> None:
        # Stands in for the infrastructure role's policy attachment.
        seen["marker"] = pulumi.CustomResource("test:index:Marker", "infrastructure-policy")
        _service(depends_on=[seen["marker"]], opts=pulumi.ResourceOptions(transformations=[record]))

    build(construct)
    options = seen[EXPRESS]
    assert options.ignore_changes == [IMAGE_PATH] == ["primaryContainer.image"]
    assert options.depends_on is not None and seen["marker"] in options.depends_on


# --- Registry and logs ----------------------------------------------------


def test_the_registry_refuses_to_move_a_tag_and_scans_what_is_pushed():
    """A mutable tag lets a second push move `:abc123` to different bytes, and then neither a
    rollback nor an incident timeline can say what ran."""
    for config, force_delete in ((CONFIG, True), (PROD, False)):
        resources = build(partial(Registry, config, app=SLUG))
        repository = only(resources, "aws:ecr/repository:Repository")
        assert repository.inputs["name"] == config.name(SLUG)
        assert repository.inputs["imageTagMutability"] == "IMMUTABLE"
        assert repository.inputs["imageScanningConfiguration"] == {"scanOnPush": True}
        assert repository.inputs["encryptionConfigurations"] == [{"encryptionType": "AES256"}]
        # A dev destroy succeeds with images in it; production keeps the only copy of what runs.
        assert repository.inputs["forceDelete"] is force_delete
        policy = only(resources, "aws:ecr/lifecyclePolicy:LifecyclePolicy").json("policy")
        assert policy["rules"][0]["selection"]["countNumber"] == 30


def test_every_log_group_expires_what_it_keeps():
    """A group without retention keeps every line forever, and storage is billed."""
    for config, days in ((CONFIG, 30), (PROD, 90)):
        resources = build(partial(Logs, config, apps=[SLUG, f"{SLUG}-migrate"]))
        groups = [r for r in resources if r.typ == "aws:cloudwatch/logGroup:LogGroup"]
        assert {g.inputs["name"] for g in groups} == {
            f"/ecs/{config.name(SLUG)}",
            f"/ecs/{config.name(SLUG, 'migrate')}",
        }
        assert {g.inputs["retentionInDays"] for g in groups} == {days}


# --- Bootstrap and teardown -----------------------------------------------


def _script(*parts: str) -> str:
    return INFRA.joinpath(*parts).read_text()


def test_the_bootstrap_script_creates_what_the_stack_looks_up():
    """The stack looks up the state key by alias and the OIDC provider by URL, and the preview role
    is scoped to the bucket by name. If any of the three drifts from what `state-bucket.sh`
    creates, the first `pulumi preview` fails on a lookup that points at nothing."""
    from config import GITHUB_OIDC_URL, STATE_KEY_ALIAS

    for name in ("state-bucket.sh", "teardown.sh"):
        script = _script("bootstrap", name)
        assert f'KEY_ALIAS="{STATE_KEY_ALIAS}"' in script, name
        assert f'OIDC_URL="{GITHUB_OIDC_URL}"' in script, name
        assert 'BUCKET="pulumi-state-${ACCOUNT}-${REGION}"' in script, name
    assert CONFIG.state_bucket == f"pulumi-state-{ACCOUNT}-{REGION}"
    backend = (PULUMI_DIR / "Pulumi.yaml").read_text()
    assert "url: s3://pulumi-state-" in backend


def test_the_bootstrap_script_refuses_the_wrong_account():
    """Everything it creates is account-level and shared; the shell's current account is not
    necessarily the one this repository was generated for."""
    script = _script("bootstrap", "state-bucket.sh")
    assert "aws sts get-caller-identity" in script
    assert '"${CALLER}" != "${ACCOUNT}"' in script
    assert script.index("get-caller-identity") < script.index("create-key")


def test_the_state_bucket_is_locked_down_on_every_run():
    script = _script("bootstrap", "state-bucket.sh")
    for setting in (
        "BlockPublicAcls=true,IgnorePublicAcls=true,BlockPublicPolicy=true,RestrictPublicBuckets=true",
        "ObjectOwnership=BucketOwnerEnforced",
        "Status=Enabled",
        '"SSEAlgorithm":"aws:kms"',
        '"BucketKeyEnabled":true',
        '"aws:SecureTransport":"false"',
        '"NoncurrentDays":90',
        '"DaysAfterInitiation":7',
        "enable-key-rotation",
        "LocationConstraint=",
    ):
        assert setting in script, setting


def test_the_teardown_script_cannot_strand_a_stack():
    """Deleting the state bucket while any stack still holds resources leaves those resources
    running, billing, and with no record of what they are. Checking rather than warning is the
    whole reason this is a script, and the confirmation is the same bet `pulumi up` makes: refuse
    rather than guess when nothing can answer."""
    script = _script("bootstrap", "teardown.sh")
    assert "stack export" in script, "the emptiness check is the guard; do not remove it"
    assert "stack ls --all" in script, (
        "without --all, `stack ls` returns only this Pulumi project's stacks and another "
        "project's live stack is absent rather than under-counted — see ADR-0013"
    )
    assert "! -t 0" in script, "must refuse when nothing can confirm"
    assert '"${reply}" != "${BUCKET}"' in script, "must make a human type the bucket name"


def test_the_teardown_script_cannot_delete_a_backend_it_shares():
    """Pulumi standards MUST-8. The bucket is named for the account and region alone, so a second
    service generated into the same account bootstraps onto it by construction. Ownership is a
    different question from resource count, and it is answered by listing the backend first."""
    script = _script("bootstrap", "teardown.sh")
    assert '--prefix ".pulumi/stacks/"' in script
    assert "PULUMI_PROJECT" in script
    listing = script.index('--prefix ".pulumi/stacks/"')
    assert listing < script.index("delete-objects"), "list the backend before deleting anything"
    assert listing < script.index("schedule-key-deletion")


def test_the_teardown_script_keeps_an_oidc_provider_something_still_trusts():
    """One per account, shared by every pipeline in it — Pulumi-managed or not."""
    script = _script("bootstrap", "teardown.sh")
    assert "list-roles" in script and "AssumeRolePolicyDocument" in script
    assert script.index("list-roles") < script.index("delete-open-id-connect-provider")
