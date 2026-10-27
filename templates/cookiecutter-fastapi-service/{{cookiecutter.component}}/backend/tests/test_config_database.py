"""The database half of the settings contract.

Every case passes the password explicitly. Reading it from the ambient environment would make these
tests pass or fail depending on whose shell they run in, which is the single most common cause of
"green locally, red in CI".

`DATABASE_AUTH` has one mode per way a process proves who it is: `password` everywhere a password
exists, and {% if cookiecutter.deploy_target == "aws-ecs-express" %}`rds-iam`{% else %}`cloud-sql-iam`{% endif %} for the deployed service, which has none. Each mode reads its
own settings and is refused at start-up without them, rather than at its first connection.
"""

from __future__ import annotations

from typing import Any

import pytest
from sqlalchemy.engine import make_url

from app.core.config import PLACEHOLDER_SECRET, Settings
{%- if cookiecutter.deploy_target == "aws-ecs-express" %}

#: What the ECS task definition sets for `rds-iam`, and nothing else.
IAM: dict[str, Any] = {
    "database_auth": "rds-iam",
    "database_host": "app-pg-dev.abcdefghijkl.{{ cookiecutter.aws_region }}.rds.amazonaws.com",
    "database_port": 5432,
    "database_name": "app",
    "database_user": "app_iam",
    "aws_region": "{{ cookiecutter.aws_region }}",
}
{%- else %}

#: What the Cloud Run revision sets for `cloud-sql-iam`, and nothing else.
IAM: dict[str, Any] = {
    "database_auth": "cloud-sql-iam",
    "cloud_sql_instance": "{{ cookiecutter.gcp_project_id }}:{{ cookiecutter.gcp_region }}:app-pg-dev",
    "database_name": "app",
    "database_user": "app-run-dev@{{ cookiecutter.gcp_project_id }}.iam",
}
{%- endif %}

#: The settings an IAM mode must be given, as opposed to the port, which has a default.
REQUIRED = [key for key in IAM if key not in ("database_auth", "database_port")]


def test_placeholder_password_is_rejected_when_deployed() -> None:
    """The point of the validator: a placeholder credential in production *starts*, and a service
    that starts with the wrong credentials fails later and much further from the cause."""
    with pytest.raises(ValueError, match="POSTGRES_PASSWORD"):
        Settings(environment="production", postgres_password=PLACEHOLDER_SECRET)


def test_placeholder_password_is_fine_locally() -> None:
    settings = Settings(environment="local", postgres_password=PLACEHOLDER_SECRET)

    assert settings.database_auth == "password"
    assert settings.database_url.startswith("postgresql+asyncpg://")


def test_override_wins_over_the_parts() -> None:
    """A full DSN, when given, is used whole: neither set of parts is consulted."""
    settings = Settings(
        environment="production",
        postgres_password=PLACEHOLDER_SECRET,
        database_host="ignored",
        database_name="ignored",
        database_user="ignored",
        database_url_override="postgresql+asyncpg://u:p@/db?host=/cloudsql/x",
    )

    assert settings.database_url == "postgresql+asyncpg://u:p@/db?host=/cloudsql/x"


def test_the_deployed_parts_replace_the_local_ones_whole_never_mixed() -> None:
    """`DATABASE_HOST` switches from the `POSTGRES_*` parts — what Compose and the test runner set —
    to the `DATABASE_*` ones, all of them. A mix would be a deployed host with a local password."""
    local = Settings(environment="test", postgres_host="compose-db", postgres_user="compose")
    assert make_url(local.database_url).host == "compose-db"

    deployed = Settings(
        environment="production",
        postgres_host="compose-db",
        postgres_user="compose",
        postgres_password="compose-password",
        database_host="db.internal",
        database_name="app",
        database_user="admin",
        database_password="from-the-secret",
    )
    url = make_url(deployed.database_url)
    assert (url.host, url.port, url.username, url.password) == (
        "db.internal",
        5432,
        "admin",
        "from-the-secret",
    )


@pytest.mark.parametrize("missing", ["database_name", "database_user"])
def test_the_deployed_parts_must_all_be_given(missing: str) -> None:
    parts = {"database_host": "db.internal", "database_name": "app", "database_user": "admin"}
    del parts[missing]
    with pytest.raises(ValueError, match=missing.upper()):
        Settings(environment="test", **parts)


def test_a_deployed_password_must_be_set_when_the_deployed_parts_are_used() -> None:
    with pytest.raises(ValueError, match="DATABASE_PASSWORD"):
        Settings(
            environment="production",
            postgres_password="a-real-one",
            database_host="db.internal",
            database_name="app",
            database_user="admin",
        )


def test_a_password_from_a_secret_is_escaped_into_the_dsn() -> None:
    """A generated password may hold any character the generator allows. An f-string DSN splits on
    the first `@` or `/` in it, and the failure is an authentication error naming the wrong host."""
    settings = Settings(
        environment="production",
        database_host="db.internal",
        database_user="app",
        database_password="p@ss/w:rd%",
        database_name="app",
    )

    url = make_url(settings.database_url)
    assert url.password == "p@ss/w:rd%"
    assert (url.host, url.port, url.database) == ("db.internal", 5432, "app")


def test_a_socket_directory_travels_as_the_host_parameter() -> None:
    """Cloud Run mounts a Cloud SQL instance as a Unix socket under `/cloudsql/`, and asyncpg reads
    a socket path from the `host` query parameter, never from the authority. The port stays: it
    names the socket file."""
    settings = Settings(
        environment="production",
        database_host="/cloudsql/project:region:instance",
        database_name="app",
        database_user="postgres",
        database_password="from-secret-manager",
    )

    url = make_url(settings.database_url)
    assert url.host is None
    assert url.port == 5432
    assert url.query["host"] == "/cloudsql/project:region:instance"


def test_the_iam_mode_builds_a_dsn_with_no_password() -> None:
    settings = Settings(environment="production", **IAM)

    url = make_url(settings.database_url)
    assert url.password is None
    assert url.username == IAM["database_user"]
    assert url.database == "app"
{%- if cookiecutter.deploy_target == "aws-ecs-express" %}
    assert url.host == IAM["database_host"]
    # An IAM token is a bearer credential; RDS also refuses anything but TLS.
    assert url.query == {"ssl": "require"}
{%- else %}
    # The connector supplies the address and the TLS; there is nothing to dial here.
    assert url.host is None
{%- endif %}


def test_the_iam_mode_needs_no_password_when_deployed() -> None:
    """The placeholder check is about passwords, and this mode has none to check — including the
    local `POSTGRES_PASSWORD`, which it never reads."""
    Settings(environment="production", postgres_password=PLACEHOLDER_SECRET, **IAM)


@pytest.mark.parametrize("missing", REQUIRED)
def test_the_iam_mode_refuses_to_start_without_each_of_its_settings(missing: str) -> None:
    """An IAM mode on an empty host, user or {% if cookiecutter.deploy_target == "aws-ecs-express" %}region{% else %}instance{% endif %} is a deploy that cannot connect."""
    settings = {key: value for key, value in IAM.items() if key != missing}

    with pytest.raises(ValueError, match=r"requires " + missing.upper()):
        Settings(environment="production", **settings)


def test_the_iam_mode_refuses_a_credential_it_would_ignore() -> None:
    with pytest.raises(ValueError, match="takes no DATABASE_PASSWORD or DATABASE_URL_OVERRIDE"):
        Settings(environment="production", database_password="left-over", **IAM)
    with pytest.raises(ValueError, match="takes no DATABASE_PASSWORD or DATABASE_URL_OVERRIDE"):
        Settings(environment="production", database_url_override="postgresql://x", **IAM)
{%- if cookiecutter.deploy_target == "gcp-cloud-run" %}


@pytest.mark.parametrize("instance", ["app-pg-dev", "project:app-pg-dev", "project::app-pg-dev"])
def test_the_iam_mode_refuses_an_instance_that_is_not_a_connection_name(instance: str) -> None:
    with pytest.raises(ValueError, match="project:region:instance"):
        Settings(environment="production", **{**IAM, "cloud_sql_instance": instance})


def test_a_domain_scoped_project_is_a_connection_name() -> None:
    Settings(environment="production", **{**IAM, "cloud_sql_instance": "example.com:p:r:i"})
{%- endif %}


def test_an_unknown_auth_mode_is_refused() -> None:
    with pytest.raises(ValueError, match="database_auth"):
        Settings(environment="production", database_auth="{% if cookiecutter.deploy_target == "aws-ecs-express" %}cloud-sql-iam{% else %}rds-iam{% endif %}")


def test_a_port_that_is_not_one_is_refused() -> None:
    with pytest.raises(ValueError, match="DATABASE_PORT"):
        Settings(environment="production", **{**IAM, "database_port": 70000})


@pytest.mark.parametrize(
    ("role", "auth"), [("app_iam", "rds-iam"), ("svc-run-dev@my-project.iam", "cloud-sql-iam")]
)
def test_an_app_role_the_grant_script_can_quote_is_accepted(role: str, auth: str) -> None:
    settings = Settings(environment="local", database_app_role=role, database_app_auth=auth)

    assert (settings.database_app_role, settings.database_app_auth) == (role, auth)


@pytest.mark.parametrize("role", ["app'; DROP TABLE users; --", "a$$b", "x" * 64])
def test_an_app_role_the_grant_script_cannot_quote_is_refused(role: str) -> None:
    """The role name is embedded in the migration's grant script. Refused here, at start-up, rather
    than trusted to the quoting there."""
    with pytest.raises(ValueError, match="DATABASE_APP_ROLE"):
        Settings(environment="local", database_app_role=role, database_app_auth="rds-iam")


def test_the_app_role_and_how_it_logs_in_come_together() -> None:
    """A role with no way to log in is a role nothing uses; a way to log in with no role is a grant
    to nobody."""
    with pytest.raises(ValueError, match="DATABASE_APP_ROLE requires DATABASE_APP_AUTH"):
        Settings(environment="local", database_app_role="app_iam")
    with pytest.raises(ValueError, match="DATABASE_APP_AUTH requires DATABASE_APP_ROLE"):
        Settings(environment="local", database_app_auth="rds-iam")
