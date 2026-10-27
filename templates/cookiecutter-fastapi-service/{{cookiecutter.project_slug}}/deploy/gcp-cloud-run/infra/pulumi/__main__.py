"""Composition root.

Reads top to bottom as a dependency graph: APIs first because everything needs them, then the
things with no dependencies, then the service that ties them together.

App-specific configuration lives here rather than inside `components/service.py`. The component
describes "a Cloud Run service in this project"; what makes this one what it is — a port, a probe
path, a database, a migration job — is an argument.

Exports are named for what consumes them. Every one becomes a GitHub repository variable, and
`infra/scripts/sync-github.sh` reads them straight out of `pulumi stack output`. The names on the
left of that script must match `.github/workflows/cd.yml` exactly; a stack exporting
`workload_identity_provider` to a pipeline expecting `WIF_PROVIDER` is a deploy that never runs and
never says why.
"""

from __future__ import annotations

import pulumi
import pulumi_gcp as gcp

import config as stack_config
{%- if cookiecutter.use_postgres == "yes" %}
from components import Apis, Database, Identities, JobSpec, Registry, Secrets, Service
from components.database import ADMIN_USER, DB_NAME
{%- else %}
from components import Apis, Identities, Registry, Secrets, Service
{%- endif %}

config = stack_config.load()

provider = gcp.Provider("gcp", project=config.project, region=config.region)
opts = pulumi.ResourceOptions(provider=provider)

project = gcp.organizations.get_project_output(
    project_id=config.project, opts=pulumi.InvokeOptions(provider=provider)
)

apis = Apis(config, opts=opts)
# Everything below needs the APIs enabled first, or the first `up` fails halfway through.
after_apis = pulumi.ResourceOptions.merge(opts, pulumi.ResourceOptions(depends_on=apis.services))

registry = Registry(config, opts=after_apis)
identities = Identities(config, project_number=project.number, opts=after_apis)
{%- if cookiecutter.use_postgres == "yes" %}
# The service logs in as its runtime service account; the stack creates that IAM database user.
database = Database(config, iam_service_account_email=identities.runtime.email, opts=after_apis)
{%- endif %}

# Every secret is an empty container read by exactly one account, and gets a version out of band
# (Pulumi standards MUST-3, ADR-0023). Nothing here generates or holds a value.
{%- if cookiecutter.use_postgres == "yes" %}
#
# * `jwt-secret` — the HS256 key the service signs access tokens with. `just infra-secrets`
#   generates it, so nobody chooses it, pastes it into a ticket or makes it twelve characters long.
#   The service reads it; the migration job signs nothing and is not granted it.
# * `db-admin-password` — the `postgres` password the migration job connects with. The same recipe
#   generates it and sets it on the instance. Only the migrator account can read it.
{%- endif %}
{%- if cookiecutter.use_sentry == "yes" %}
{%- if cookiecutter.use_postgres == "no" %}
#
{%- endif %}
# * `sentry-dsn` — from a Sentry account, so a human adds it.
{%- endif %}
secrets = Secrets(
    config,
{%- if cookiecutter.use_postgres == "no" and cookiecutter.use_sentry == "no" %}
    # No database and no Sentry: nothing this service holds a secret for, yet. A secret added later
    # is one entry in each mapping below, and its version a key in `config.py`.
    accessors={},
    versions={},
{%- else %}
    accessors={
{%- if cookiecutter.use_postgres == "yes" %}
        "jwt-secret": identities.runtime.email,
        "db-admin-password": identities.migrator.email,
{%- endif %}
{%- if cookiecutter.use_sentry == "yes" %}
        "sentry-dsn": identities.runtime.email,
{%- endif %}
    },
    versions={
{%- if cookiecutter.use_postgres == "yes" %}
        "jwt-secret": config.jwt_secret_version,
        "db-admin-password": config.db_admin_password_version,
{%- endif %}
{%- if cookiecutter.use_sentry == "yes" %}
        "sentry-dsn": config.sentry_dsn_version,
{%- endif %}
    },
{%- endif %}
    opts=after_apis,
)

#: The deployed environment. Values the application reads and a human may need to reason about;
#: anything credential-shaped is a secret mount instead.
#:
#: `ENVIRONMENT` is the *application's* environment and not this stack's, so it comes from
#: `appEnvironment` in `Pulumi.<stack>.yaml` rather than from a literal here. A literal is how a
#: stack called `dev` deploys a service that believes it is production, serves no `/docs`, and
#: says nothing on the page about why. `config.py` has the distinction in full.
RUNTIME_ENV = {
    "ENVIRONMENT": config.app_environment,
    "LOG_LEVEL": "INFO",
    # JSON everywhere a log aggregator reads it.
    "JSON_LOGS": "true",
    # Gunicorn reads this natively. More workers on a single shared vCPU is more copies of the same
    # import graph competing for one core, not more throughput.
    "WEB_CONCURRENCY": "2",
}

#: Environment variable name -> the logical secret it is mounted from.
#:
#: A secret is mounted only once its version number is in stack configuration, because an empty
#: container fails the deploy outright — see `components/secrets.py`.
SECRET_ENV: dict[str, str] = {}
{%- if cookiecutter.use_postgres == "yes" %}
if config.jwt_secret_version:
    SECRET_ENV["JWT_SECRET"] = "jwt-secret"
{%- endif %}
{%- if cookiecutter.use_sentry == "yes" %}
if config.sentry_dsn_version:
    SECRET_ENV["SENTRY_DSN"] = "sentry-dsn"
{%- endif %}

#: What the backend service runs with: the runtime environment plus the settings for a browser
#: client (`StackConfig.browser_env`){% if cookiecutter.use_postgres == "yes" %} and how the service reaches the database{% endif %}.
#: Kept separate from `RUNTIME_ENV` because the migration job shares that dict, and the browser
#: settings are about HTTP, which the job does not serve.
BACKEND_ENV: dict[str, pulumi.Input[str]] = {**RUNTIME_ENV, **config.browser_env}
{%- if cookiecutter.use_postgres == "yes" %}
# The connector, as the runtime service account's IAM database user: no password, no socket, no
# Cloud SQL volume. See `app/core/database.py` in the backend.
BACKEND_ENV |= {
    "DATABASE_AUTH": "cloud-sql-iam",
    "CLOUD_SQL_INSTANCE": database.connection_name,
    "DATABASE_NAME": DB_NAME,
    "DATABASE_USER": database.iam_user_name,
}

#: What the migration job runs with. The admin login over the Unix socket Cloud Run's built-in
#: Cloud SQL connection mounts at `/cloudsql` — a connector too, which connector enforcement
#: admits — with the password mounted from `db-admin-password` once its version is configured.
#: `DATABASE_APP_ROLE` is the service's IAM user, which every run grants the tables to;
#: `DATABASE_APP_AUTH` says Cloud SQL created it, so the migration checks for it rather than
#: creating it.
MIGRATE_ENV: dict[str, pulumi.Input[str]] = {
    **RUNTIME_ENV,
    "DATABASE_AUTH": "password",
    "DATABASE_HOST": database.connection_name.apply(lambda name: f"/cloudsql/{name}"),
    "DATABASE_NAME": DB_NAME,
    "DATABASE_USER": ADMIN_USER,
    "DATABASE_APP_ROLE": database.iam_user_name,
    "DATABASE_APP_AUTH": "cloud-sql-iam",
}
MIGRATE_SECRET_ENV: dict[str, str] = {}
if config.db_admin_password_version:
    MIGRATE_SECRET_ENV["DATABASE_PASSWORD"] = "db-admin-password"
{%- endif %}

backend = Service(
    config,
    app=config.slug,
    runtime_email=identities.runtime.email,
    # The production image sets PORT=8080 and gunicorn binds it. Cloud Run may override PORT, and
    # the container reads it from the environment rather than hard-coding one.
    port=8080,
    # Liveness, not readiness. Readiness may legitimately be false on a first deploy.
    health_path="/health/live",
    env=BACKEND_ENV,
    secret_env=SECRET_ENV,
    secret_refs=secrets.refs,
{%- if cookiecutter.use_postgres == "yes" %}
    jobs=[
        JobSpec(
            name="migrate",
            command=["alembic"],
            args=["upgrade", "head"],
            env=MIGRATE_ENV,
            secret_env=MIGRATE_SECRET_ENV,
            service_account=identities.migrator.email,
            cloud_sql_instance=database.connection_name,
        )
    ],
{%- endif %}
    # Depends on the grants that let each account read its secrets: a revision started before its
    # account can read a mounted secret fails to start, and reports nothing useful. The versions
    # themselves exist before their numbers can be configured — `just infra-secrets` adds a
    # version and only then records it.
    opts=pulumi.ResourceOptions.merge(
        after_apis, pulumi.ResourceOptions(depends_on=secrets.grants)
    ),
)

# --- Outputs. These are the CD pipeline's configuration. ---------------------
pulumi.export("gcp_project", config.project)
pulumi.export("gcp_region", config.region)
pulumi.export("image_repo", registry.url)
pulumi.export("wif_provider", identities.provider_name)
pulumi.export("deploy_sa", identities.ci.email)
pulumi.export("runtime_sa", identities.runtime.email)
pulumi.export("cloud_run_service", backend.service.name)
pulumi.export("service_url", backend.url)
# Not synced to GitHub. `"true"` once every secret the service and its migration need has a pinned
# version; `sync-github.sh` refuses to un-gate CD until it is, and names `just infra-secrets` as the
# fix. A string, because every other export is one and a repository variable can only be one.
pulumi.export("secrets_ready", "true" if config.secrets_ready else "false")
{%- if cookiecutter.use_postgres == "yes" %}
pulumi.export("cloud_run_migrate_job", backend.jobs["migrate"].name)
pulumi.export("cloud_sql_instance", database.connection_name)
{%- else %}
# No database, so no migration job. Exported empty rather than omitted: `cd.yml` skips the migrate
# step on an empty variable, and a variable that is absent rather than empty is indistinguishable
# from one somebody forgot to set.
pulumi.export("cloud_run_migrate_job", "")
pulumi.export("cloud_sql_instance", "")
{%- endif %}
