"""The whole program, run against mocks: what AWS would be asked for, and what CD is told.

`stack.deploy` is the composition root, so running it is running the stack — every resource, every
lookup and every export, as generated, with a browser client configured, and in a production-shaped
stack. The assertions read the requests the mocks recorded, which is what AWS would receive, rather
than the program's source.

The second half is the contract with the deploy pipeline. The failure it exists to prevent: a stack
exporting `deploy_role` and a pipeline reading `vars.AWS_DEPLOY_ROLE_ARN`. Nothing errors. The
deploy job is simply skipped forever, because it is gated on a variable nothing ever sets.
"""

from __future__ import annotations

import dataclasses
import re
from dataclasses import dataclass
from functools import cache
from pathlib import Path
from typing import Any

import pulumi

from tests.mocks import (
    ACCOUNT,
{%- if cookiecutter.use_postgres == "yes" %}
    MASTER_SECRET_ARN,
{%- endif %}
    MOCKS,
    OIDC_ARN,
    REGION,
    SLUG,
    STATE_KEY_ARN,
    Called,
    Registered,
)

# Importing tests.mocks installed the mocks. The program has to be imported after it.
MOCKS.reset()

import stack  # noqa: E402 - after set_mocks
from config import StackConfig  # noqa: E402 - after set_mocks

PULUMI_DIR = Path(__file__).resolve().parent.parent
INFRA = PULUMI_DIR.parent
REPO = INFRA.parent
GITHUB_REPOSITORY = "{{ cookiecutter.github_repository }}"

CONFIG = StackConfig(
    account_id=ACCOUNT,
    region=REGION,
    environment="dev",
    github_repository=GITHUB_REPOSITORY,
    slug=SLUG,
    pulumi_project=SLUG,
)

EXPRESS = "aws:ecs/expressGatewayService:ExpressGatewayService"
#: A Secrets Manager VersionId: a UUID. Not an f-string, and not doubled braces either — this file
#: is rendered by cookiecutter, and Jinja would read a doubled brace as an expression.
VERSION_ID = r"[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}"
ROLE = "aws:iam/role:Role"
ROLE_POLICY = "aws:iam/rolePolicy:RolePolicy"
ATTACHMENT = "aws:iam/rolePolicyAttachment:RolePolicyAttachment"


@dataclass(frozen=True)
class Deployment:
    exports: dict[str, Any]
    resources: tuple[Registered, ...]
    calls: tuple[Called, ...]

    def of_type(self, typ: str) -> list[Registered]:
        return [r for r in self.resources if r.typ == typ]

    def one(self, typ: str, name: str) -> Registered:
        matches = [r for r in self.of_type(typ) if r.name == name]
        assert len(matches) == 1, f"{typ} {name!r}: {len(matches)} registered"
        return matches[0]

    def role(self, label: str) -> Registered:
        return self.one(ROLE, f"{label}-role")

    def statements(self, policy: str) -> list[dict[str, Any]]:
        return list(self.one(ROLE_POLICY, policy).json("policy")["Statement"])


@cache
def deployed(**overrides: Any) -> Deployment:
    """Run the whole program once per configuration, and keep what it asked for.

    The exports are read through an apply the program awaits — the value Pulumi would record —
    rather than off the outputs after the run, when an output that belongs to no resource may not
    have resolved yet.
    """
    MOCKS.reset()
    exports: dict[str, Any] = {}

    def program() -> pulumi.Output[None]:
        outputs = stack.deploy(dataclasses.replace(CONFIG, **overrides))
        return pulumi.Output.all(
            **{name: pulumi.Output.from_input(value) for name, value in outputs.items()}
        ).apply(exports.update)

    pulumi.runtime.test(program)()
    return Deployment(dict(exports), tuple(MOCKS.resources), tuple(MOCKS.calls))


def backend() -> Deployment:
    return deployed()


#: A browser client's origin: what `corsOrigins` holds once a React app is deployed in front.
ORIGIN = "https://d111111abcdef8.cloudfront.net"


def with_browser() -> Deployment:
    """Configured for a browser client — its origin allowed{% if cookiecutter.use_postgres == "yes" %}, the refresh token in a cookie{% endif %}."""
    return deployed(cors_origins=(ORIGIN,){% if cookiecutter.use_postgres == "yes" %}, refresh_transport="cookie"{% endif %})


def production() -> Deployment:
    return deployed(environment="prod")


#: A Secrets Manager VersionId, as `just infra-secrets` would pin one.
PINNED = "6f1e3c2a-9b8d-4e7f-a1c2-3d4e5f6a7b8c"


def pinned() -> Deployment:
    """After `just infra-secrets`: every required secret has a version in stack configuration."""
{%- if cookiecutter.use_postgres == "yes" %}
    return deployed(jwt_secret_version=PINNED)
{%- else %}
    return deployed()
{%- endif %}


# --- The provider ---------------------------------------------------------


def test_every_aws_resource_and_lookup_uses_the_one_explicit_provider():
    """A resource on a default provider reads whatever profile and region the shell has, and
    `pulumi:disable-default-providers` would refuse it at apply time rather than here."""
    for deployment in (backend(), with_browser()):
        aws_resources = [r for r in deployment.resources if r.typ.startswith("aws:")]
        assert aws_resources
        for resource in aws_resources:
            assert "pulumi:providers:aws::aws::" in resource.provider, (resource.typ, resource.name)
        lookups = [c for c in deployment.calls if c.token.startswith("aws:")]
        assert {c.token.split(":")[-1] for c in lookups} >= {
            "getAvailabilityZones",
            "getOpenIdConnectProvider",
            "getKey",
        }
        for call in lookups:
            assert "pulumi:providers:aws::aws::" in call.provider, call.token
    assert '"aws"' in (PULUMI_DIR / "Pulumi.yaml").read_text().split("disable-default-providers")[1]


def test_the_provider_refuses_any_other_account_and_tags_everything():
    """`allowed_account_ids` fails a run with the wrong credentials before it plans anything."""
    provider = backend().one("pulumi:providers:aws", "aws")
    assert provider.json("allowedAccountIds") == [ACCOUNT]
    assert provider.inputs["region"] == REGION
    assert provider.json("defaultTags") == {
        "tags": {
            "app": SLUG,
            "environment": "dev",
            "managed-by": "pulumi",
            "pulumi-stack": "dev",
        }
    }


# --- Exports --------------------------------------------------------------

#: Every name the stack exports, in every configuration. Empty when the thing does not exist.
EXPORTS = {
    "aws_account_id",
    "aws_region",
    "image_repo",
    "deploy_role_arn",
    "preview_role_arn",
    "runtime_role_arn",
    "github_subject_prefix",
    "ecs_cluster",
    "ecs_service",
    "ecs_service_arn",
    "service_url",
    "ecs_task_subnets",
    "ecs_task_security_group",
    "ecs_migrate_task_family",
    "rds_instance",
    "secrets_ready",
}


def test_every_export_exists_in_every_configuration():
    """An export that exists only sometimes is a variable sync-github.sh cannot tell from a typo."""
    for deployment in (backend(), with_browser(), production(), pinned()):
        assert set(deployment.exports) == EXPORTS


def test_secrets_are_ready_only_once_every_required_version_is_pinned():
    """The export sync-github.sh gates CD on. A string, like every other export."""
{%- if cookiecutter.use_postgres == "yes" %}
    assert backend().exports["secrets_ready"] == "false"
{%- else %}
    # No database means no users, so no signing key: nothing to wait for.
    assert backend().exports["secrets_ready"] == "true"
{%- endif %}
    assert pinned().exports["secrets_ready"] == "true"


def test_every_export_the_pipeline_requires_has_a_value():
    """sync-github.sh stops on an empty required output, so one that a fresh apply leaves empty is
    a pipeline that can never be configured."""
    for deployment in (backend(), with_browser()):
        for name in _required_outputs():
            assert deployment.exports[name], name
    exports = backend().exports
    assert exports["aws_account_id"] == ACCOUNT
    assert exports["image_repo"] == f"{ACCOUNT}.dkr.ecr.{REGION}.amazonaws.com/{SLUG}-dev"
    assert exports["service_url"] == f"https://{SLUG}-dev.ecs.{REGION}.on.aws"
    assert exports["ecs_task_subnets"] == "public-0-id,public-1-id"
    assert exports["ecs_task_security_group"] == "app-id"
{%- if cookiecutter.use_postgres == "yes" %}
    assert exports["ecs_migrate_task_family"] == f"{SLUG}-migrate-dev"
    assert exports["rds_instance"] == CONFIG.db_identifier()
{%- else %}
    assert exports["ecs_migrate_task_family"] == ""
    assert exports["rds_instance"] == ""
{%- endif %}


# --- Network --------------------------------------------------------------


def test_tasks_run_in_public_subnets_and_there_is_no_nat_gateway():
    """Express Mode puts the load balancer and the tasks in the same subnets, and a public IP is the
    tasks' way to ECR, Secrets Manager and CloudWatch without a NAT gateway's standing charge."""
    deployment = backend()
    assert not deployment.of_type("aws:ec2/natGateway:NatGateway")
    # Sorted by name: registration order is the event loop's, not the program's.
    subnets = sorted(deployment.of_type("aws:ec2/subnet:Subnet"), key=lambda r: r.name)
    public = [s for s in subnets if s.name.startswith("public")]
    assert len(public) == CONFIG.availability_zone_count
    assert all(s.inputs["mapPublicIpOnLaunch"] is True for s in public)
    # The first zones in sorted order, so the choice does not move between runs.
    assert [s.inputs["availabilityZone"] for s in public] == [f"{REGION}a", f"{REGION}b"]
    assert [s.inputs["cidrBlock"] for s in public] == ["10.0.0.0/24", "10.0.1.0/24"]
    routes = deployment.of_type("aws:ec2/route:Route")
    assert [(r.inputs["routeTableId"], r.inputs["destinationCidrBlock"]) for r in routes] == [
        ("public-routes-id", "0.0.0.0/0")
    ]
    assert routes[0].inputs["gatewayId"] == "internet-gateway-id"


def test_the_app_security_group_admits_nothing():
    """Express Mode's own group admits its load balancer. Ours is an identity, not a door."""
    deployment = backend()
    ingress = deployment.of_type("aws:vpc/securityGroupIngressRule:SecurityGroupIngressRule")
    assert all(rule.inputs["securityGroupId"] != "app-id" for rule in ingress)
    for service in deployment.of_type(EXPRESS):
        assert service.inputs["networkConfigurations"] == [
            {"subnets": ["public-0-id", "public-1-id"], "securityGroups": ["app-id"]}
        ]
{%- if cookiecutter.use_postgres == "yes" %}


# --- Database -------------------------------------------------------------


def test_the_database_is_unreachable_from_outside_the_vpc():
    """Not publicly accessible, in subnets with no route out, behind a group that admits Postgres
    from the service's group and nothing else. Any one of the three is a one-line mistake."""
    deployment = backend()
    instance = deployment.one("aws:rds/instance:Instance", "postgres")
    assert instance.inputs["publiclyAccessible"] is False
    assert instance.inputs["storageEncrypted"] is True
    assert instance.inputs["vpcSecurityGroupIds"] == ["db-id"]

    group = deployment.one("aws:rds/subnetGroup:SubnetGroup", "db-subnets")
    assert instance.inputs["dbSubnetGroupName"] == group.inputs["name"]
    assert group.inputs["subnetIds"] == ["isolated-0-id", "isolated-1-id"]
    isolated = [
        a
        for a in deployment.of_type("aws:ec2/routeTableAssociation:RouteTableAssociation")
        if a.inputs["subnetId"].startswith("isolated")
    ]
    assert {a.inputs["routeTableId"] for a in isolated} == {"isolated-routes-id"}
    assert all(
        r.inputs["routeTableId"] != "isolated-routes-id"
        for r in deployment.of_type("aws:ec2/route:Route")
    )

    rules = [
        r
        for r in deployment.of_type("aws:vpc/securityGroupIngressRule:SecurityGroupIngressRule")
        if r.inputs["securityGroupId"] == "db-id"
    ]
    assert len(rules) == 1
    rule = rules[0].inputs
    assert rule["referencedSecurityGroupId"] == "app-id"
    assert (rule["ipProtocol"], rule["fromPort"], rule["toPort"]) == ("tcp", 5432, 5432)
    assert "cidrIpv4" not in rule
    egress = deployment.of_type("aws:vpc/securityGroupEgressRule:SecurityGroupEgressRule")
    assert all(r.inputs["securityGroupId"] != "db-id" for r in egress)


def test_every_connection_to_the_database_is_tls():
    """RDS refuses anything else; the migration asks for it, and `rds-iam` always does."""
    deployment = backend()
    parameters = deployment.one("aws:rds/parameterGroup:ParameterGroup", "db-parameters")
    assert parameters.inputs["family"] == "postgres16"
    assert {(p["name"], p["value"]) for p in parameters.inputs["parameters"]} == {
        ("rds.force_ssl", "1")
    }
    assert _env(_migrate_container(deployment), "environment")["PGSSLMODE"] == "require"
    assert _env(_backend_container(deployment))["DATABASE_AUTH"] == "rds-iam"


def test_no_database_password_is_in_the_program_its_configuration_or_its_state():
    """Pulumi standards MUST-3, ADR-0023. RDS generates, stores and rotates the master password;
    the stack sees only its secret's ARN. The service has no password at all."""
    instance = backend().one("aws:rds/instance:Instance", "postgres").inputs
    assert instance["manageMasterUserPassword"] is True
    assert instance["iamDatabaseAuthenticationEnabled"] is True
    assert "password" not in instance
    assert instance["username"] == "app"
    for deployment in (backend(), pinned()):
        types = {r.typ for r in deployment.resources}
        assert "aws:secretsmanager/secretVersion:SecretVersion" not in types
        assert not [t for t in types if t.startswith("random:")]


def test_the_service_logs_in_with_an_iam_token_and_holds_no_password():
    """`rds-iam`: the backend signs a token with the task role for every new connection. Nothing
    credential-shaped is in its environment or its secrets."""
    container = _backend_container(pinned())
    env = _env(container)
    assert env["DATABASE_AUTH"] == "rds-iam"
    host = f"{CONFIG.db_identifier()}.abcdefghijkl.{REGION}.rds.amazonaws.com"
    assert env["DATABASE_HOST"] == host
    assert (env["DATABASE_PORT"], env["DATABASE_NAME"]) == ("5432", "app")
    assert env["DATABASE_USER"] == "app_iam"
    assert env["AWS_REGION"] == REGION
    names = {e["name"] for e in container.get("environments", [])} | {
        s["name"] for s in container.get("secrets", [])
    }
    assert not names & {"DATABASE_URL_OVERRIDE", "DATABASE_PASSWORD", "POSTGRES_PASSWORD"}


def test_the_runtime_role_may_log_in_as_the_service_role_and_nothing_else():
    """`rds-db:connect` on one database user of one instance, named by its resource id — which RDS
    never reuses — rather than its identifier, which a re-created instance would."""
    deployment = backend()
    role = deployment.role("runtime").inputs["name"]
    assert not [a for a in deployment.of_type(ATTACHMENT) if a.inputs["role"] == role]
    policies = [p for p in deployment.of_type(ROLE_POLICY) if p.inputs["role"] == role]
    assert [p.name for p in policies] == ["runtime-connects-to-database"]
    (statement,) = deployment.statements("runtime-connects-to-database")
    assert statement["Action"] == "rds-db:connect"
    assert statement["Resource"] == (
        f"arn:aws:rds-db:{REGION}:{ACCOUNT}:dbuser:db-ABCDEFGHIJKLMNOPQRSTUVWXYZ/app_iam"
    )
    renamed = deployed(database_app_role="svc_login")
    (statement,) = renamed.statements("runtime-connects-to-database")
    assert statement["Resource"].endswith("/svc_login")
    assert _env(_backend_container(renamed))["DATABASE_USER"] == "svc_login"
    assert _env(_migrate_container(renamed), "environment")["DATABASE_APP_ROLE"] == "svc_login"


def test_only_the_migration_task_can_read_the_master_credentials():
    """A separate execution role, because ECS reads a task's secrets with its execution role: given
    to the service's, the master password would be one task-definition edit from the service. And
    no `kms:Decrypt` — RDS uses the AWS-managed `aws/secretsmanager` key, whose policy already lets
    the account decrypt through Secrets Manager."""
    deployment = backend()
    (statement,) = deployment.statements("migrate-reads-master-secret")
    assert statement == {
        "Effect": "Allow",
        "Action": "secretsmanager:GetSecretValue",
        "Resource": MASTER_SECRET_ARN,
    }
    for policy in deployment.of_type(ROLE_POLICY):
        if policy.name == "migrate-reads-master-secret":
            continue
        assert MASTER_SECRET_ARN not in policy.inputs["policy"], policy.name
    migrate_role = deployment.role("migrate-exec").inputs["name"]
    task = deployment.one("aws:ecs/taskDefinition:TaskDefinition", f"{SLUG}-migrate")
    assert task.inputs["executionRoleArn"] == f"arn:aws:iam::{ACCOUNT}:role/{migrate_role}"
    for service in deployment.of_type(EXPRESS):
        assert not service.inputs["executionRoleArn"].endswith(f"/{migrate_role}")


def test_deletion_protection_and_a_final_snapshot_only_in_production():
    """A dev destroy has to finish; a production destroy has to be impossible by accident."""
    dev = backend().one("aws:rds/instance:Instance", "postgres").inputs
    assert dev["deletionProtection"] is False
    assert dev["skipFinalSnapshot"] is True
    assert "finalSnapshotIdentifier" not in dev
    assert dev["applyImmediately"] is True

    prod = production().one("aws:rds/instance:Instance", "postgres").inputs
    assert prod["deletionProtection"] is True
    assert prod["skipFinalSnapshot"] is False
    assert prod["finalSnapshotIdentifier"] == f"{SLUG}-final-prod"
    assert prod["applyImmediately"] is False


def test_the_database_uses_the_cheapest_honest_instance():
    instance = backend().one("aws:rds/instance:Instance", "postgres").inputs
    assert instance["instanceClass"] == "db.t4g.micro"
    assert instance["allocatedStorage"] == 20
    assert instance["storageType"] == "gp3"
    assert instance["multiAz"] is False
    assert instance["engineVersion"] == "16"
    assert instance["caCertIdentifier"] == "rds-ca-rsa2048-g1"


def _migrate_container(deployment: Deployment) -> dict[str, Any]:
    task = deployment.one("aws:ecs/taskDefinition:TaskDefinition", f"{SLUG}-migrate")
    (container,) = task.json("containerDefinitions")
    return dict(container)


def test_the_migration_task_gets_the_master_login_and_nothing_else():
    """It signs nothing, so it is not handed the signing key — a task holding a credential it never
    uses is a credential with a second place to leak from. It calls no AWS API, so it has no task
    role. And its login is the master user's, read from RDS's own secret by JSON key with no
    version: the one deliberate exception to pinning, because RDS rotates that secret and a pin
    would break the migration at the first rotation."""
    task = pinned().one("aws:ecs/taskDefinition:TaskDefinition", f"{SLUG}-migrate")
    assert task.inputs["family"] == f"{SLUG}-migrate-dev"
    assert task.inputs["requiresCompatibilities"] == ["FARGATE"]
    assert task.inputs["networkMode"] == "awsvpc"
    assert "taskRoleArn" not in task.inputs
    (container,) = task.json("containerDefinitions")
    assert container["command"] == ["alembic", "upgrade", "head"]
    assert {s["name"]: s["valueFrom"] for s in container["secrets"]} == {
        "DATABASE_USER": f"{MASTER_SECRET_ARN}:username::",
        "DATABASE_PASSWORD": f"{MASTER_SECRET_ARN}:password::",
    }
    env = _env(container, "environment")
    assert env["DATABASE_AUTH"] == "password"
    assert env["DATABASE_HOST"].endswith(f".{REGION}.rds.amazonaws.com")
    assert (env["DATABASE_PORT"], env["DATABASE_NAME"]) == ("5432", "app")
    assert env["DATABASE_APP_ROLE"] == "app_iam", "every run grants the service role the tables"
    assert env["DATABASE_APP_AUTH"] == "rds-iam", "and creates it first, if it is missing"
    assert container["logConfiguration"]["options"]["awslogs-group"] == f"/ecs/{SLUG}-migrate-dev"
    source = (PULUMI_DIR / "components" / "migrations.py").read_text()
    assert "one secret reference in this stack that is not pinned" in source
{%- endif %}


# --- Secrets --------------------------------------------------------------


def _assert_pinned(value_from: str) -> None:
    """`<secret ARN>:::<VersionId>` — never the bare ARN, which is AWSCURRENT."""
    arn = rf"arn:aws:secretsmanager:{REGION}:{ACCOUNT}:secret:[a-z0-9-]+-AbCdEf"
    assert re.fullmatch(arn + ":::" + VERSION_ID, value_from), value_from


def _backend_container(deployment: Deployment) -> dict[str, Any]:
    return dict(deployment.one(EXPRESS, SLUG).inputs["primaryContainer"])


def test_every_secret_reference_pins_a_version():
    """ADR-0016's rule on ECS: a task reads its secrets when it starts, so an unpinned reference
    lets one deployment's tasks hold different values after a rotation."""
    for deployment in (backend(), with_browser(), production(), pinned()):
        for service in deployment.of_type(EXPRESS):
            for secret in service.inputs["primaryContainer"].get("secrets", []):
                _assert_pinned(secret["valueFrom"])
{%- if cookiecutter.use_postgres == "yes" %}
    (jwt,) = _backend_container(pinned())["secrets"]
    assert jwt["name"] == "JWT_SECRET"
    assert jwt["valueFrom"].endswith(f"-jwt-secret-dev-AbCdEf:::{PINNED}")


def test_nothing_is_referenced_until_its_version_is_pinned():
    """A reference to a secret with no value is a task that cannot start. Before `just
    infra-secrets` the service references nothing — and a server with no signing key refuses to
    start, which is why sync-github.sh keeps CD gated until `secrets_ready` says otherwise."""
    assert not _backend_container(backend()).get("secrets")
{%- endif %}


def test_secrets_are_deleted_at_once_outside_production():
    """A secret pending deletion keeps its name, so a dev destroy and re-create would collide."""
    for deployment, window in ((backend(), 0), (production(), 30)):
        for secret in deployment.of_type("aws:secretsmanager/secret:Secret"):
            assert secret.inputs["recoveryWindowInDays"] == window, secret.name
{%- if cookiecutter.use_sentry == "yes" %}


def test_sentry_is_not_referenced_until_its_version_is_configured():
    """An ECS secret pointing at a secret with no value is a task that cannot start."""
    names = {s["name"] for s in _backend_container(backend()).get("secrets", [])}
    assert "SENTRY_DSN" not in names
    version = "3f5c8a0e-1b2d-4c3e-9f8a-7b6c5d4e3f2a"
    container = _backend_container(deployed(sentry_dsn_version=version))
    (sentry,) = [s for s in container["secrets"] if s["name"] == "SENTRY_DSN"]
    assert sentry["valueFrom"].endswith(f"-sentry-dsn-dev-AbCdEf:::{version}")
{%- endif %}


# --- Identities -----------------------------------------------------------


def _trust(deployment: Deployment, label: str) -> dict[str, Any]:
    (statement,) = deployment.role(label).json("assumeRolePolicy")["Statement"]
    return dict(statement)


def test_the_deploy_role_trusts_one_repository_in_one_environment():
    """Without the `sub` condition any repository on GitHub could deploy here; with a wildcard,
    any branch of this one could, without the production environment's protection rules."""
    trust = _trust(backend(), "deploy")
    assert trust["Principal"] == {"Federated": OIDC_ARN}
    assert trust["Action"] == "sts:AssumeRoleWithWebIdentity"
    assert trust["Condition"] == {
        "StringEquals": {
            "token.actions.githubusercontent.com:aud": "sts.amazonaws.com",
            "token.actions.githubusercontent.com:sub": (
                f"repo:{GITHUB_REPOSITORY}:environment:production"
            ),
        }
    }


def test_the_preview_role_trusts_pull_requests_from_one_repository():
    trust = _trust(backend(), "preview")
    assert trust["Principal"] == {"Federated": OIDC_ARN}
    assert trust["Condition"] == {
        "StringEquals": {
            "token.actions.githubusercontent.com:aud": "sts.amazonaws.com",
            "token.actions.githubusercontent.com:sub": f"repo:{GITHUB_REPOSITORY}:pull_request",
        }
    }


def test_both_roles_trust_the_immutable_subject_when_it_is_configured():
    """GitHub issues `repo:<owner>@<id>/<repo>@<id>` for many repositories; a role that trusts the
    name-only form then admits no token at all."""
    deployment = deployed(github_subject_prefix="repo:acme@123/billing-api@456")
    for label, suffix in (("deploy", ":environment:production"), ("preview", ":pull_request")):
        condition = _trust(deployment, label)["Condition"]["StringEquals"]
        assert condition["token.actions.githubusercontent.com:sub"] == (
            "repo:acme@123/billing-api@456" + suffix
        )


def test_the_task_roles_trust_ecs_in_this_account_only():
    """`aws:SourceAccount` is the confused-deputy guard."""
    for label in ("execution", "runtime"{% if cookiecutter.use_postgres == "yes" %}, "migrate-exec"{% endif %}):
        trust = _trust(backend(), label)
        assert trust["Principal"] == {"Service": "ecs-tasks.amazonaws.com"}
        assert trust["Condition"] == {"StringEquals": {"aws:SourceAccount": ACCOUNT}}
    assert _trust(backend(), "infrastructure")["Principal"] == {"Service": "ecs.amazonaws.com"}


def test_the_runtime_role_is_the_services_task_role{% if cookiecutter.use_postgres == "no" %}_and_starts_with_no_permissions{% endif %}():
    """The application's own identity. A grant goes here when the application needs one."""
    deployment = backend()
    role = deployment.role("runtime").inputs["name"]
    task_role = deployment.one(EXPRESS, SLUG).inputs["taskRoleArn"]
    assert task_role == f"arn:aws:iam::{ACCOUNT}:role/{role}"
{%- if cookiecutter.use_postgres == "no" %}
    assert not [a for a in deployment.of_type(ATTACHMENT) if a.inputs["role"] == role]
    assert not [p for p in deployment.of_type(ROLE_POLICY) if p.inputs["role"] == role]
{%- endif %}


def test_the_execution_role_reads_exactly_this_stacks_secrets():
    deployment = backend()
    secrets = {r.name for r in deployment.of_type("aws:secretsmanager/secret:Secret")}
    if not secrets:
        assert not [
            p for p in deployment.of_type(ROLE_POLICY) if p.name == "execution-reads-secrets"
        ]
        return
    (statement,) = deployment.statements("execution-reads-secrets")
    assert statement["Action"] == "secretsmanager:GetSecretValue"
    assert len(statement["Resource"]) == len(secrets)
    assert all(
        arn.startswith(f"arn:aws:secretsmanager:{REGION}:{ACCOUNT}:secret:{SLUG}-")
        for arn in statement["Resource"]
    )


def _actions(statement: dict[str, Any]) -> list[str]:
    action = statement["Action"]
    return [action] if isinstance(action, str) else list(action)


def _resources(statement: dict[str, Any]) -> list[str]:
    resource = statement["Resource"]
    return [resource] if isinstance(resource, str) else list(resource)


#: The three deploy actions IAM accepts only on `*`.
NO_RESOURCE_LEVEL = {
    "ecr:GetAuthorizationToken",
    "ecs:DescribeTaskDefinition",
    "ecs:RegisterTaskDefinition",
}


def test_the_deploy_role_acts_only_on_this_stacks_resources():
    """Least privilege, checked per statement: no wildcard action, and `*` as a resource only for
    the actions IAM offers no resource-level control for. `ecr:*` on `*` would let CI delete every
    repository in the account."""
    for deployment in (backend(), with_browser()):
        for statement in deployment.statements("deploy-permissions"):
            actions = _actions(statement)
            assert all("*" not in action for action in actions), actions
            if "*" in _resources(statement):
                assert set(actions) <= NO_RESOURCE_LEVEL, actions
            if any(a.startswith("ecr:") for a in actions) and "*" in _resources(statement):
                assert actions == ["ecr:GetAuthorizationToken"]


def test_the_deploy_role_can_deploy_the_service_and_push_its_image():
    deployment = backend()
    statements = deployment.statements("deploy-permissions")

    def resources_for(action: str) -> list[str]:
        return [r for s in statements if action in _actions(s) for r in _resources(s)]

    services = {s.inputs["serviceName"] for s in deployment.of_type(EXPRESS)}
    assert services == {f"{SLUG}-dev"}
    updatable = resources_for("ecs:UpdateExpressGatewayService")
    assert {arn.rsplit("/", 1)[-1] for arn in updatable} == services
    assert {arn.rsplit("/", 1)[-1] for arn in resources_for("ecr:PutImage")} == services
    passed = resources_for("iam:PassRole")
    assert {arn.rsplit("/", 1)[-1] for arn in passed} == {
{%- if cookiecutter.use_postgres == "yes" %}
        CONFIG.role_name(label)
        for label in ("execution", "runtime", "infrastructure", "migrate-exec")
{%- else %}
        CONFIG.role_name(label) for label in ("execution", "runtime", "infrastructure")
{%- endif %}
    }
    (pass_role,) = [s for s in statements if "iam:PassRole" in _actions(s)]
    assert pass_role["Condition"] == {
        "StringEquals": {"iam:PassedToService": ["ecs-tasks.amazonaws.com", "ecs.amazonaws.com"]}
    }
{%- if cookiecutter.use_postgres == "yes" %}
    (run_task,) = [s for s in statements if "ecs:RunTask" in _actions(s)]
    assert run_task["Resource"] == (
        f"arn:aws:ecs:{REGION}:{ACCOUNT}:task-definition/{SLUG}-migrate-dev:*"
    )
    assert run_task["Condition"] == {
        "ArnEquals": {"ecs:cluster": f"arn:aws:ecs:{REGION}:{ACCOUNT}:cluster/{SLUG}-dev"}
    }
{%- else %}
    assert not resources_for("ecs:RunTask"), "no database, so no migration to run"
{%- endif %}


def test_the_preview_role_reads_everything_and_writes_only_its_lock():
    """A preview that could write state, or anything in the account, would be an apply with the
    review step removed."""
    deployment = backend()
    role = deployment.role("preview").inputs["name"]
    managed = {
        a.inputs["policyArn"] for a in deployment.of_type(ATTACHMENT) if a.inputs["role"] == role
    }
    assert managed == {"arn:aws:iam::aws:policy/ReadOnlyAccess"}

    bucket = f"arn:aws:s3:::pulumi-state-{ACCOUNT}-{REGION}"
    for statement in deployment.statements("preview-reads-state"):
        actions = _actions(statement)
        writes = [
            a for a in actions if re.match(r"\w+:(Put|Delete|Create|Update|Register|Run|Pass)", a)
        ]
        if writes:
            assert set(writes) <= {"s3:PutObject", "s3:DeleteObject"}, writes
            assert _resources(statement) == [f"{bucket}/.pulumi/locks/organization/{SLUG}/*"]
        if any(a.startswith("kms:") for a in actions):
            assert _resources(statement) == [STATE_KEY_ARN]
        if "s3:GetObject" in actions and not writes:
            assert set(_resources(statement)) == {
                f"{bucket}/.pulumi/meta.yaml",
                f"{bucket}/.pulumi/stacks/{SLUG}/*",
            }
    # No secret's value, for any secret: the stack declares no secret version, so a refresh never
    # reads one, and a pull request must not be able to.
    for deployment in (backend(), pinned()):
        for statement in deployment.statements("preview-reads-state"):
            assert "secretsmanager:GetSecretValue" not in _actions(statement)


# --- The Express services -------------------------------------------------


def test_one_express_service_and_one_repository():
    """A browser client is its own repository and stack (ADR-0022); this one runs the API."""
    for deployment in (backend(), with_browser()):
        assert [s.name for s in deployment.of_type(EXPRESS)] == [SLUG]
        assert len(deployment.of_type("aws:ecr/repository:Repository")) == 1


def _env(container: dict[str, Any], key: str = "environments") -> dict[str, str]:
    """A container's plain environment: `environments` on an Express service, `environment` in
    a task definition."""
    return {e["name"]: e["value"] for e in container.get(key, [])}


def test_no_browser_may_call_the_service_until_an_origin_is_configured():
    """CORS off is the safe default: an allowlist nobody chose is a hole nobody reviewed."""
    assert "CORS_ORIGINS" not in _env(_backend_container(backend()))


def test_the_configured_origins_reach_the_service():
    """Otherwise the SPA loads, makes exactly one request, and the browser refuses the answer —
    which reads as an API outage rather than as a missing allowlist entry."""
    assert _env(_backend_container(with_browser()))["CORS_ORIGINS"] == ORIGIN
    two = deployed(cors_origins=(ORIGIN, "http://localhost:3000"))
    assert _env(_backend_container(two))["CORS_ORIGINS"] == f"{ORIGIN},http://localhost:3000"
{%- if cookiecutter.use_postgres == "yes" %}


def test_the_refresh_transport_reaches_the_service():
    assert _env(_backend_container(backend()))["REFRESH_TRANSPORT"] == "body"
    assert _env(_backend_container(with_browser()))["REFRESH_TRANSPORT"] == "cookie"


def test_the_browser_settings_never_reach_the_migration_task():
    """They are about HTTP, which a migration does not serve. And the application refuses
    `REFRESH_TRANSPORT=cookie` without `CORS_ORIGINS`, so a task holding one without the other is a
    task that exits on its first settings load."""
    task = with_browser().one("aws:ecs/taskDefinition:TaskDefinition", f"{SLUG}-migrate")
    (container,) = task.json("containerDefinitions")
    names = {e["name"] for e in container.get("environment", [])}
    assert names, "the migration task still gets the runtime environment"
    assert not names & {"CORS_ORIGINS", "REFRESH_TRANSPORT"}, names
{%- endif %}


def test_the_backend_runs_with_the_runtime_environment():
    env = _env(_backend_container(backend()))
    assert env == {
        "ENVIRONMENT": "staging",
        "LOG_LEVEL": "INFO",
        "JSON_LOGS": "true",
        "WEB_CONCURRENCY": "2",
{%- if cookiecutter.use_postgres == "yes" %}
        "REFRESH_TRANSPORT": "body",
        "DATABASE_AUTH": "rds-iam",
        "DATABASE_HOST": f"{CONFIG.db_identifier()}.abcdefghijkl.{REGION}.rds.amazonaws.com",
        "DATABASE_PORT": "5432",
        "DATABASE_NAME": "app",
        "DATABASE_USER": "app_iam",
        "AWS_REGION": REGION,
{%- endif %}
    }


def test_every_service_waits_for_its_grants_and_its_secret_values():
    """A component's `depends_on` orders the component; the services take theirs directly. The
    infrastructure role's attachment above all: AWS documents a DRAINING race on delete without
    it, and a task started before its execution role can read secrets fails to start."""
    from components import Identities
    from components.identities import INFRASTRUCTURE_POLICY, TASK_EXECUTION_POLICY

    urns: list[str] = []

    def program() -> pulumi.Output[None]:
        identities = Identities(
            CONFIG,
            github_provider_arn=OIDC_ARN,
            state_key_arn=STATE_KEY_ARN,
            secret_arns=["arn:aws:secretsmanager:r:1:secret:s"],
        )
        dependencies = [d.urn for d in identities.service_dependencies]
        return pulumi.Output.all(*dependencies).apply(urns.extend)

    pulumi.runtime.test(program)()
    assert {urn.rsplit("::", 1)[-1] for urn in urns} == {
        f"infrastructure-{INFRASTRUCTURE_POLICY.rsplit('/', 1)[-1]}",
        f"execution-{TASK_EXECUTION_POLICY.rsplit('/', 1)[-1]}",
        "execution-reads-secrets",
    }
    source = (PULUMI_DIR / "stack.py").read_text()
    assert "list(identities.service_dependencies)" in source
    assert source.count("depends_on=after_grants") == 1
{%- if cookiecutter.use_postgres == "yes" %}
    # The migration task waits for its own role's grants — the master secret above all.
    assert "depends_on=identities.migrate_dependencies" in source
    names = {
        r.name
        for r in backend().resources
        if r.typ in (ROLE_POLICY, ATTACHMENT) and r.inputs["role"].endswith("-migrate-exec-dev")
    }
    assert names == {
        "migrate-reads-master-secret",
        f"migrate-exec-{TASK_EXECUTION_POLICY.rsplit('/', 1)[-1]}",
    }
{%- endif %}


def test_every_log_group_has_a_retention_and_every_runner_writes_to_one():
    deployment = backend()
    groups = {
        g.inputs["name"]: g.inputs["retentionInDays"]
        for g in deployment.of_type("aws:cloudwatch/logGroup:LogGroup")
    }
    assert set(groups.values()) == {30}
    for service in deployment.of_type(EXPRESS):
        (logs,) = service.inputs["primaryContainer"]["awsLogsConfigurations"]
        assert logs["logGroup"] in groups


def test_the_cluster_buys_container_insights_only_in_production():
    def insights(deployment: Deployment) -> str:
        (setting,) = deployment.one("aws:ecs/cluster:Cluster", "cluster").inputs["settings"]
        assert setting["name"] == "containerInsights"
        return str(setting["value"])

    assert insights(backend()) == "disabled"
    assert insights(production()) == "enhanced"


# --- The contract with the deploy pipeline --------------------------------

#: Repository variables a human sets by hand, because they come from outside this stack.
MANUAL_VARIABLES = {
    "SENTRY_ORG",
    "SENTRY_PROJECT",
}

#: Every workflow that deploys something, and the variable that gates it.
GATES = {
    "cd.yml": "AWS_DEPLOY_ROLE_ARN",
}


def _workflow(name: str) -> str:
    return (REPO / ".github" / "workflows" / name).read_text()


def _deploy_workflows() -> list[str]:
    names = [n for n in GATES if (REPO / ".github" / "workflows" / n).exists()]
    assert "cd.yml" in names
    return names


def _sync_script() -> str:
    return (INFRA / "scripts" / "sync-github.sh").read_text()


def _mapping(block: str) -> dict[str, str]:
    body = re.search(rf"declare -a {block}=\((.*?)\n\)", _sync_script(), re.S)
    assert body, block
    return dict(re.findall(r'"([a-z_]+):([A-Z0-9_]+)"', body.group(1)))


def _required_outputs() -> set[str]:
    return set(_mapping("REQUIRED"))


def _not_synced() -> set[str]:
    body = re.search(r"declare -a NOT_SYNCED=\(([^)]*)\)", _sync_script())
    assert body
    return set(body.group(1).split())


def test_every_variable_a_deploy_reads_is_one_the_sync_script_sets():
    provided = set(_mapping("REQUIRED").values()) | set(_mapping("OPTIONAL").values())
    for name in [*_deploy_workflows(), "infra.yml"]:
        referenced = set(re.findall(r"vars\.([A-Z0-9_]+)", _workflow(name))) - MANUAL_VARIABLES
        assert referenced <= provided, f"{name}: never set: {sorted(referenced - provided)}"


def test_every_output_the_sync_script_reads_is_one_the_stack_exports():
    mapped = set(_mapping("REQUIRED")) | set(_mapping("OPTIONAL"))
    assert mapped <= set(backend().exports), (
        f"never exported: {sorted(mapped - set(backend().exports))}"
    )


def test_every_export_is_synced_or_deliberately_not():
    """An export nobody syncs and nobody listed is either dead or a variable somebody forgot."""
    mapped = set(_mapping("REQUIRED")) | set(_mapping("OPTIONAL"))
    assert _not_synced() == {
        "runtime_role_arn",
        "rds_instance",
        "github_subject_prefix",
        "secrets_ready",
    }
    assert set(backend().exports) == mapped | _not_synced()
    assert not mapped & _not_synced()


def test_cd_is_not_ungated_until_every_secret_has_a_pinned_version():
    """A deploy before then is tasks that refuse to start — no signing key. The script checks the
    stack's own answer before it sets a single variable, and names the fix."""
    script = _sync_script()
    assert "read_output secrets_ready" in script
    assert '"${SECRETS_READY}" != "true"' in script
    assert "just infra-secrets" in script
    assert script.index("secrets_ready") < script.index("gh variable set")
{%- if cookiecutter.use_postgres == "yes" %}


def _secrets_script() -> str:
    return (INFRA / "scripts" / "secrets.sh").read_text()


def test_the_secrets_script_names_and_pins_what_the_stack_reads():
    """It fills the secret the stack created and sets the key `config.py` reads. A name that
    drifts on either side is a version added to a secret nothing references."""
    script = _secrets_script()
    assert 'echo "${SLUG}-$1-${ENVIRONMENT}"' in script
    assert CONFIG.name(CONFIG.slug, "jwt-secret") == f"{SLUG}-jwt-secret-dev"
    assert "fill jwt-secret jwtSecretVersion" in script
    assert 'config.get("jwtSecretVersion")' in (PULUMI_DIR / "config.py").read_text()
    assert '"${CALLER}" != "${ACCOUNT}"' in script, "it refuses another account's shell"
    justfile = (REPO / "justfile").read_text()
    assert "infra-secrets STACK" in justfile
    assert "./infra/scripts/secrets.sh" in justfile


def test_the_secrets_script_never_puts_a_value_where_it_can_be_read():
    """Not printed, not an argument, not a file: generated into a variable and handed on stdin by
    `printf`, a builtin, so it never reaches a process list."""
    script = _secrets_script()
    uses = [line.strip() for line in script.splitlines() if "${value}" in line]
    assert uses == ['version="$(printf \'%s\' "${value}" | add_version "${name}")"']
    assert "--secret-string file:///dev/stdin" in script
    assert "set -x" not in script
    assert "--rotate" in script and "signs every user out" in script
{%- endif %}


def test_every_deploy_stays_inert_until_it_is_configured():
    """A committed deploy path that cannot fire by accident is reviewed alongside the code it
    ships, rather than written under pressure on the day."""
    for name in _deploy_workflows():
        assert f"if: vars.{GATES[name]} != ''" in _workflow(name), name
    assert "vars.AWS_PREVIEW_ROLE_ARN != ''" in _workflow("infra.yml")


def test_every_deploy_assumes_a_role_and_holds_no_key():
    for name in [*_deploy_workflows(), "infra.yml"]:
        workflow = _workflow(name)
        assert "id-token: write" in workflow, name
        assert "aws-actions/configure-aws-credentials@" in workflow, name
        assert "aws-access-key-id" not in workflow and "AWS_SECRET_ACCESS_KEY" not in workflow, name
    # The preview assumes the read-only role, never the one that can deploy.
    preview = _workflow("infra.yml").partition("preview:")[2]
    assert "vars.AWS_PREVIEW_ROLE_ARN" in preview and "AWS_DEPLOY_ROLE_ARN" not in preview
    assert "environment: production" in _workflow("cd.yml"), "the deploy role trusts only this"


def test_every_deploy_is_filtered_to_the_component_it_deploys():
    """Without a filter, a typo fix in a README rebuilds and redeploys a container."""
    for name in _deploy_workflows():
        assert "paths:" in _workflow(name), name
    # The contract test reads the deploy workflows, so a change to one has to run it.
    infra = _workflow("infra.yml")
    assert "'.github/workflows/cd.yml'" in infra


def test_every_deploy_tags_the_image_with_the_commit_rather_than_latest():
    """A rollback has to be able to name an image. `:latest` is a guess about what it pointed at
    last Tuesday, and the repository refuses to move a tag anyway."""
    for name in _deploy_workflows():
        workflow = _workflow(name)
        steps = [line for line in workflow.splitlines() if not line.lstrip().startswith("#")]
        assert ":latest" not in "\n".join(steps), name
        assert "github.sha" in workflow, name


def test_every_deploy_sends_back_the_whole_container_with_only_the_image_changed():
    for name in _deploy_workflows():
        workflow = _workflow(name)
        assert ".service.activeConfigurations[0].primaryContainer | .image = $image" in workflow
        assert "update-express-gateway-service" in workflow
        assert "--primary-container file://" in workflow
        # `wait services-stable` gives up after ten minutes, and a healthy Express canary has taken
        # longer than that: the job would report a failed deploy that succeeded.
        assert "rolloutState" in workflow
        assert "wait services-stable" not in workflow


def test_no_deploy_workflow_carries_a_cookiecutter_variable():
    """`.github/workflows/` is `_copy_without_render`, because a GitHub Actions expression and a
    Jinja one are written with the same two braces. A cookiecutter variable in one of these files
    is therefore never substituted: it survives into the generated repository as literal text."""
    # The patterns are written with a character class around the second brace so that this file —
    # which *is* rendered — contains no Jinja delimiter of its own.
    for name in [*_deploy_workflows(), "infra.yml"]:
        workflow = _workflow(name)
        assert not re.search(r"\{[{]\s*cookiecutter", workflow), name
        assert not re.search(r"\{[%]", workflow), name
{%- if cookiecutter.use_postgres == "yes" %}


def test_the_migration_runs_before_the_deploy_and_fails_it_on_a_nonzero_exit():
    workflow = _workflow("cd.yml")
    assert "if: vars.ECS_MIGRATE_TASK_FAMILY != ''" in workflow
    assert workflow.index("name: Migrate") < workflow.index("name: Deploy")
    assert "aws ecs wait tasks-stopped" in workflow
    assert '"$EXIT_CODE" != "0"' in workflow
    assert "logs get-log-events" in workflow
{%- endif %}
