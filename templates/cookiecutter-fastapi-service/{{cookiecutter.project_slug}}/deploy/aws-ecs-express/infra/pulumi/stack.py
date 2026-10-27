"""The composition root, as a function.

Reads top to bottom as a dependency graph: the provider and the three things the stack looks up,
then the things with no dependencies, then the service that ties them together, then the one grant
that has to name it.

A function rather than module-level code so the tests can run the whole program against mocks, in
every configuration that changes what it declares, and read every export the pipeline will be
configured from. `__main__.py` is three lines: load the configuration, call this, export what it
returns.

App-specific configuration lives here rather than inside `components/service.py`. The component
describes "an Express service in this stack"; what makes this one what it is — a port, a health
path, a database, a migration task — is an argument.

Exports are named for what consumes them. Every one becomes a GitHub repository variable, and
`infra/scripts/sync-github.sh` reads them straight out of `pulumi stack output`. The names on the
left of that script must match `.github/workflows/cd.yml` exactly; a stack exporting
`deploy_role` to a pipeline expecting `AWS_DEPLOY_ROLE_ARN` is a deploy that never runs and never
says why.
"""

from __future__ import annotations

import pulumi
import pulumi_aws as aws

from components import (
    Cluster,
{%- if cookiecutter.use_postgres == "yes" %}
    Database,
{%- endif %}
    DeployTargets,
    Identities,
    Logs,
{%- if cookiecutter.use_postgres == "yes" %}
    Migrations,
{%- endif %}
    Network,
    Registry,
    Secrets,
    Service,
)
{%- if cookiecutter.use_postgres == "yes" %}
from components.database import DB_NAME
from components.identities import DatabaseAccess
from components.migrations import current_json_key
{%- endif %}
from config import GITHUB_OIDC_URL, STATE_KEY_ALIAS, StackConfig

#: The port the container listens on. The production image sets PORT=8080 and gunicorn binds it.
#: No PORT variable is set on the service: the image already agrees with this number, and a second
#: place to say it is a second place for it to be wrong.
PORT = 8080


def deploy(config: StackConfig) -> dict[str, pulumi.Input[str]]:
    # One explicit provider, and every resource and lookup below uses it. `allowed_account_ids` is
    # the wrong-credentials guard: a terminal holding another account's session fails here, before
    # it plans anything, rather than half-way through creating this stack in somebody else's
    # account. The default tags put every resource's owner on the resource itself.
    provider = aws.Provider(
        "aws",
        region=config.region,
        allowed_account_ids=[config.account_id],
        default_tags=aws.ProviderDefaultTagsArgs(
            tags={
                "app": config.slug,
                "environment": config.environment,
                "managed-by": "pulumi",
                "pulumi-stack": config.stack,
            }
        ),
    )
    opts = pulumi.ResourceOptions(provider=provider)
    lookup = pulumi.InvokeOptions(provider=provider)

    zones = aws.get_availability_zones_output(state="available", opts=lookup)
    # Created by infra/bootstrap/state-bucket.sh, once per account, and only looked up here: a
    # stack that owned it would delete it from under every other service on `destroy`.
    github = aws.iam.get_open_id_connect_provider_output(url=GITHUB_OIDC_URL, opts=lookup)
    # Also the bootstrap script's: the key that encrypts the state bucket and the stack's secrets.
    state_key = aws.kms.get_key_output(key_id=STATE_KEY_ALIAS, opts=lookup)

{%- if cookiecutter.use_postgres == "yes" %}

    migrate_app = f"{config.slug}-migrate"
{%- endif %}

    network = Network(config, zone_names=zones.names, {% if cookiecutter.use_postgres == "yes" %}database=True, {% endif %}opts=opts)
    cluster = Cluster(config, opts=opts)
    logs = Logs(
        config,
        apps=[
            config.slug,
{%- if cookiecutter.use_postgres == "yes" %}
            migrate_app,
{%- endif %}
        ],
        opts=opts,
    )
    registry = Registry(config, app=config.slug, opts=opts)
{%- if cookiecutter.use_postgres == "yes" %}

    assert network.db_security_group is not None
    database = Database(
        config,
        subnet_ids=network.isolated_subnet_ids,
        security_group_id=network.db_security_group.id,
        opts=opts,
    )
{%- endif %}

    # Every secret here is created empty and gets a version out of band (Pulumi standards MUST-3,
    # ADR-0023); nothing in this program generates or holds a value.
{%- if cookiecutter.use_postgres == "yes" %}
    #
    # * `jwt-secret` — the HS256 key the service signs access tokens with. `just infra-secrets`
    #   generates it, so nobody chooses it, pastes it into a ticket or makes it twelve characters
    #   long. The service reads it; the migration task signs nothing and is not granted it.
{%- endif %}
{%- if cookiecutter.use_sentry == "yes" %}
{%- if cookiecutter.use_postgres == "no" %}
    #
{%- endif %}
    # * `sentry-dsn` — from a Sentry account, so a human adds it.
{%- endif %}
{%- if cookiecutter.use_postgres == "yes" %}
    #
    # The database's master password is RDS's own secret, not one of these: see `Database`.
{%- endif %}
    secrets = Secrets(
        config,
{%- if cookiecutter.use_postgres == "no" and cookiecutter.use_sentry == "no" %}
        # No database and no Sentry: nothing this service holds a secret for, yet. A secret added
        # later is a name here, and its version a key in `config.py`.
        names=[],
        versions={},
{%- else %}
        names=[{% if cookiecutter.use_postgres == "yes" %}"jwt-secret"{% endif %}{% if cookiecutter.use_postgres == "yes" and cookiecutter.use_sentry == "yes" %}, {% endif %}{% if cookiecutter.use_sentry == "yes" %}"sentry-dsn"{% endif %}],
        versions={
{%- if cookiecutter.use_postgres == "yes" %}
            "jwt-secret": config.jwt_secret_version,
{%- endif %}
{%- if cookiecutter.use_sentry == "yes" %}
            "sentry-dsn": config.sentry_dsn_version,
{%- endif %}
        },
{%- endif %}
        opts=opts,
    )

    identities = Identities(
        config,
        github_provider_arn=github.arn,
        state_key_arn=state_key.arn,
        secret_arns=list(secrets.arns.values()),
{%- if cookiecutter.use_postgres == "yes" %}
        database=DatabaseAccess(
            resource_id=database.resource_id,
            user=config.database_app_role,
            master_secret_arn=database.master_secret_arn,
        ),
{%- endif %}
        opts=opts,
    )

    #: The deployed environment. Values the application reads and a human may need to reason
    #: about; anything credential-shaped is a secret reference instead.
    #:
    #: `ENVIRONMENT` is the *application's* environment and not this stack's, so it comes from
    #: `appEnvironment` in `Pulumi.<stack>.yaml` rather than from a literal here. A literal is how
    #: a stack called `dev` deploys a service that believes it is production, serves no `/docs`,
    #: and says nothing on the page about why. `config.py` has the distinction in full.
    runtime_env = {
        "ENVIRONMENT": config.app_environment,
        "LOG_LEVEL": "INFO",
        # JSON everywhere a log aggregator reads it.
        "JSON_LOGS": "true",
        # Gunicorn reads this natively. More workers on a quarter of a vCPU is more copies of the
        # same import graph competing for one core, not more throughput.
        "WEB_CONCURRENCY": "2",
    }

    #: Environment variable name -> the logical secret it is read from.
    #:
    #: A secret is referenced only once its version id is in stack configuration, because a
    #: reference to a secret with no value is a task that cannot start — see
    #: `components/secrets.py`.
    secret_env: dict[str, str] = {}
{%- if cookiecutter.use_postgres == "yes" %}
    if config.jwt_secret_version:
        secret_env["JWT_SECRET"] = "jwt-secret"
{%- endif %}
{%- if cookiecutter.use_sentry == "yes" %}
    if config.sentry_dsn_version:
        secret_env["SENTRY_DSN"] = "sentry-dsn"
{%- endif %}

    # A service waits for the IAM grants it runs with — a task started before its execution role
    # can read its secrets fails to start and reports nothing useful. See
    # `Identities.service_dependencies`. The secret versions themselves exist before their ids can
    # be configured: `just infra-secrets` adds a version and only then records it.
    after_grants: list[pulumi.Resource] = list(identities.service_dependencies)

    #: What the backend service runs with: the runtime environment plus the settings for a browser
    #: client (`StackConfig.browser_env`){% if cookiecutter.use_postgres == "yes" %} and how the service reaches the database{% endif %}. Separate from
    #: `runtime_env` because the migration task shares that dict, and the browser settings are about
    #: HTTP, which the task does not serve.
    backend_env: dict[str, pulumi.Input[str]] = {**runtime_env, **config.browser_env}
{%- if cookiecutter.use_postgres == "yes" %}
    # IAM database authentication: a token the backend signs with the task role for every new
    # connection, for `databaseAppRole`, over TLS. No password and no DSN anywhere — see
    # `app/core/database.py` in the backend.
    backend_env |= {
        "DATABASE_AUTH": "rds-iam",
        "DATABASE_HOST": database.address,
        "DATABASE_PORT": database.port,
        "DATABASE_NAME": DB_NAME,
        "DATABASE_USER": config.database_app_role,
        "AWS_REGION": config.region,
    }
{%- endif %}

    backend = Service(
        config,
        app=config.slug,
        cluster=cluster.name,
        subnet_ids=network.public_subnet_ids,
        security_group_ids=[network.app_security_group.id],
        infrastructure_role_arn=identities.infrastructure.arn,
        execution_role_arn=identities.execution.arn,
        task_role_arn=identities.runtime.arn,
        log_group=logs.groups[config.slug].name,
        port=PORT,
        # Liveness, not readiness. Readiness may legitimately be false on a first deploy.
        health_path="/health/live",
        env=backend_env,
        secret_env=secret_env,
        secret_refs=secrets.refs,
        depends_on=after_grants,
        opts=opts,
    )
{%- if cookiecutter.use_postgres == "yes" %}

    # The master login, from the secret RDS owns: unpinned on purpose — see
    # `components/migrations.py`. And nothing else: the migration signs no token, so it is not
    # handed the key that signs them; a process holding a credential it never uses is a second
    # place for that credential to leak from. `DATABASE_APP_ROLE` is the role every run creates if
    # missing and grants the tables to.
    assert identities.migrate_execution is not None
    migrations = Migrations(
        config,
        app=config.slug,
        command=["alembic", "upgrade", "head"],
        execution_role_arn=identities.migrate_execution.arn,
        log_group=logs.groups[migrate_app].name,
        env={
            **runtime_env,
            "DATABASE_AUTH": "password",
            "DATABASE_HOST": database.address,
            "DATABASE_PORT": database.port,
            "DATABASE_NAME": DB_NAME,
            # libpq's own variable, which asyncpg honours too. RDS refuses anything but TLS
            # (`rds.force_ssl`).
            "PGSSLMODE": "require",
            "DATABASE_APP_ROLE": config.database_app_role,
            # The migration creates the role if it is missing, and grants it `rds_iam`.
            "DATABASE_APP_AUTH": "rds-iam",
        },
        secrets={
            "DATABASE_USER": current_json_key(database.master_secret_arn, "username"),
            "DATABASE_PASSWORD": current_json_key(database.master_secret_arn, "password"),
        },
        depends_on=identities.migrate_dependencies,
        opts=opts,
    )
{%- endif %}

    passed_roles = [identities.execution, identities.runtime, identities.infrastructure]
{%- if cookiecutter.use_postgres == "yes" %}
    passed_roles.append(identities.migrate_execution)
{%- endif %}
    identities.grant_deploy(
        DeployTargets(
            cluster_name=cluster.name,
            cluster_arn=cluster.arn,
            repository_arns=[registry.arn],
            service_arns=[backend.arn],
            passed_role_arns=[role.arn for role in passed_roles],
{%- if cookiecutter.use_postgres == "yes" %}
            migrate_family=migrations.family,
            migrate_log_group=logs.groups[migrate_app].name,
{%- endif %}
        )
    )

    # --- Outputs. These are the CD pipeline's configuration. -------------------
    #
    # Every export exists in every configuration — empty when the thing does not exist — so an
    # optional variable is deleted by sync-github.sh rather than left naming something gone, and so
    # the contract test can read one list of names.
    return {
        "aws_account_id": config.account_id,
        "aws_region": config.region,
        "image_repo": registry.url,
        "deploy_role_arn": identities.deploy.arn,
        "preview_role_arn": identities.preview.arn,
        # Not synced to GitHub: no workflow acts as the application. Exported because it is where
        # an S3 or SQS grant goes when the application needs one, and the ARN is what a bucket
        # policy names.
        "runtime_role_arn": identities.runtime.arn,
        # What the CI roles trust, so sync-github.sh can check it against the subject GitHub
        # actually issues for this repository before it un-gates the pipelines.
        "github_subject_prefix": config.subject_prefix,
        "ecs_cluster": cluster.name,
        "ecs_service": backend.name,
        "ecs_service_arn": backend.arn,
        "service_url": backend.url,
        # Where cd.yml runs the migration task: the service's own subnets and security group, so
        # the task reaches the database the same way the service does.
        "ecs_task_subnets": network.public_subnet_ids.apply(",".join),
        "ecs_task_security_group": network.app_security_group.id,
        # Not synced to GitHub. `"true"` once every secret the service needs has a pinned version;
        # sync-github.sh refuses to un-gate CD until it is, and names `just infra-secrets` as the
        # fix. A string, because every other export is one.
        "secrets_ready": "true" if config.secrets_ready else "false",
{%- if cookiecutter.use_postgres == "yes" %}
        "ecs_migrate_task_family": migrations.family,
        "rds_instance": database.identifier,
{%- else %}
        # No database, so no migration task. Exported empty rather than omitted: cd.yml skips the
        # migrate step on an empty variable, and sync-github.sh deletes one whose output is empty.
        "ecs_migrate_task_family": "",
        "rds_instance": "",
{%- endif %}
    }
