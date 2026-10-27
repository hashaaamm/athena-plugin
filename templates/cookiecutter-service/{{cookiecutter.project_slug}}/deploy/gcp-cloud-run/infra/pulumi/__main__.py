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
{%- if cookiecutter.use_postgres == "yes" %}
import pulumi_random as random
{%- endif %}

import config as stack_config
{%- if cookiecutter.use_postgres == "yes" %}
from components import Apis, Database, Identities, JobSpec, Registry, Secrets, Service
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
database = Database(config, opts=after_apis)

# The HS256 key the service signs access tokens with. Generated, never chosen: a key a human typed
# is a key a human can reuse, paste into a ticket, or make twelve characters long. 48 alphanumerics
# is well past the 32-byte floor `require_signing_key` enforces at start-up. It travels from Secret
# Manager into the runtime environment and nowhere else — in particular, the migration job below is
# not granted it, because it signs nothing.
jwt_secret = random.RandomPassword("jwt-secret", length=48, special=False, opts=after_apis)
{%- endif %}

secrets = Secrets(
    config,
    accessor_email=identities.runtime.email,
{%- if cookiecutter.use_postgres == "yes" %}
    database_url=database.database_url,
    jwt_secret=jwt_secret.result,
{%- endif %}
{%- if cookiecutter.use_sentry == "yes" %}
    manual_versions={"sentry-dsn": config.sentry_dsn_version},
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
#: The Sentry DSN is mounted only once its version number is in stack configuration, because an
#: empty container fails the deploy outright. Add the value, then set `sentryDsnVersion` — see
#: `components/secrets.py`.
SECRET_ENV: dict[str, str] = {}
{%- if cookiecutter.use_postgres == "yes" %}
SECRET_ENV["DATABASE_URL_OVERRIDE"] = "database-url"
SECRET_ENV["JWT_SECRET"] = "jwt-secret"
{%- endif %}
{%- if cookiecutter.use_sentry == "yes" %}
if config.sentry_dsn_version:
    SECRET_ENV["SENTRY_DSN"] = "sentry-dsn"
{%- endif %}

#: What the backend service runs with: the runtime environment plus the settings for a browser
#: client (`StackConfig.browser_env`). Kept separate from `RUNTIME_ENV` because the migration job
#: shares that dict, and the browser settings are about HTTP, which the job does not serve.
BACKEND_ENV = {**RUNTIME_ENV, **config.browser_env}

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
    connection_name=database.connection_name,
    jobs=[
        JobSpec(
            name="migrate",
            command=["alembic"],
            args=["upgrade", "head"],
            env=RUNTIME_ENV,
            secret_env={"DATABASE_URL_OVERRIDE": "database-url"},
        )
    ],
{%- endif %}
    # Depends on the secret *versions*, not just the containers: a service that waits only for the
    # container races the version into existence and fails with "version was not found".
    opts=pulumi.ResourceOptions.merge(
        after_apis, pulumi.ResourceOptions(depends_on=secrets.versions)
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
