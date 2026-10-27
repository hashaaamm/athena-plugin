"""How the engine authenticates, in each `DATABASE_AUTH` mode.
{% if cookiecutter.deploy_target == "aws-ecs-express" %}
The deployed mode cannot connect to anything from a test: there is no RDS instance and no task
role here. What can be tested is the wiring — that every new connection gets a credential made for
it, made off the event loop — and that a connection made that way is an ordinary one, which the
readiness check reads like any other. The suite's own database stands in for the instance, and a
fake provider hands it the test database's password where RDS would take a token.
{%- else %}
The deployed mode cannot connect to anything from a test: there is no Cloud SQL instance and no
service account here. What can be tested is the wiring — that every connection goes through the
connector as the IAM user, and that the connector is built off the event loop — and that a
connection made that way is an ordinary one, which the readiness check reads like any other. The
suite's own database stands in for the instance, reached through a fake connector.
{%- endif %}
"""

from __future__ import annotations

{% if cookiecutter.deploy_target == "gcp-cloud-run" -%}
import asyncio
{% endif -%}
import threading
{%- if cookiecutter.deploy_target == "gcp-cloud-run" %}
from collections.abc import Iterator
{%- endif %}
from typing import Any{% if cookiecutter.deploy_target == "gcp-cloud-run" %}, ClassVar{% endif %}

{% if cookiecutter.deploy_target == "gcp-cloud-run" -%}
import asyncpg
{% endif -%}
import pytest
{%- if cookiecutter.deploy_target == "aws-ecs-express" %}
from sqlalchemy import event
{%- endif %}
from sqlalchemy.ext.asyncio import AsyncEngine, async_sessionmaker

from app.core import database
from app.core.config import Settings
from app.repositories.health_repository import HealthRepository
from app.services.health_service import HealthService


async def _ready(engine: AsyncEngine) -> str:
    """What `/health/ready` would report through this engine."""
    async with async_sessionmaker(engine)() as session:
        readiness = await HealthService(HealthRepository(session), version="test").readiness()
    return readiness.status
{%- if cookiecutter.deploy_target == "aws-ecs-express" %}


HOST = "app-pg-dev.abcdefghijkl.{{ cookiecutter.aws_region }}.rds.amazonaws.com"


@pytest.fixture
def iam(monkeypatch: pytest.MonkeyPatch) -> Settings:
    """What the ECS task definition sets for `rds-iam`, with credentials boto3's default chain
    finds in the environment — no network, no account."""
    monkeypatch.setenv("AWS_ACCESS_KEY_ID", "testing")
    monkeypatch.setenv("AWS_SECRET_ACCESS_KEY", "testing")
    monkeypatch.delenv("AWS_SESSION_TOKEN", raising=False)
    monkeypatch.delenv("AWS_PROFILE", raising=False)
    return Settings(
        environment="production",
        database_auth="rds-iam",
        database_host=HOST,
        database_port=5432,
        database_name="app",
        database_user="app_iam",
        aws_region="{{ cookiecutter.aws_region }}",
    )


async def test_every_new_connection_asks_the_provider_for_its_own_password(
    settings: Settings,
) -> None:
    """An IAM token is checked only when a connection opens, and expires fifteen minutes after it
    was signed, so one cached at start-up stops working a quarter of an hour into the process. The
    provider is asked once per connection, on a worker thread — never on the loop."""
    loop_thread = threading.get_ident()
    calls: list[int] = []

    def provider() -> str:
        calls.append(threading.get_ident())
        return settings.postgres_password.get_secret_value()

    engine = database.create_engine(settings)
    database.password_per_connection(engine, provider)
    try:
        assert await _ready(engine) == "ok"
        assert await _ready(engine) == "ok"
    finally:
        await engine.dispose()

    assert len(calls) == 2, "NullPool opens a connection per session, and each asks once"
    assert loop_thread not in calls, "the provider ran on the event loop"


async def test_the_rds_iam_engine_connects_with_a_fresh_token_over_tls(iam: Settings) -> None:
    """What the driver would be handed, read at the last moment before it connects: the configured
    host and user, TLS required, and a password that is a token signed for exactly them."""
    engine = database.create_engine(iam)
    seen: list[dict[str, Any]] = []

    class InterceptedError(Exception):
        pass

    # Registered after the engine's own listener, so it sees the parameters that listener left.
    @event.listens_for(engine.sync_engine, "do_connect")
    def _capture(_dialect: Any, _record: Any, _cargs: Any, cparams: dict[str, Any]) -> None:
        seen.append(dict(cparams))
        raise InterceptedError

    for _ in range(2):
        with pytest.raises(InterceptedError):
            async with engine.connect():
                pass
    await engine.dispose()

    assert len(seen) == 2
    for cparams in seen:
        assert (cparams["host"], cparams["port"], cparams["user"]) == (HOST, 5432, "app_iam")
        assert cparams["database"] == "app"
        assert cparams["ssl"] == "require"
        # A callable, which asyncpg awaits as it connects: the token is signed then, not here.
        token = await cparams["password"]()
        assert token.startswith(f"{HOST}:5432/?Action=connect&DBUser=app_iam&")
        assert "X-Amz-Signature=" in token


def test_the_token_provider_signs_for_the_configured_region(iam: Settings) -> None:
    token = database.rds_iam_token_provider(iam)()

    assert "%2F{{ cookiecutter.aws_region }}%2Frds-db%2Faws4_request" in token
    assert "X-Amz-Expires=900" in token
{%- else %}


INSTANCE = "{{ cookiecutter.gcp_project_id }}:{{ cookiecutter.gcp_region }}:app-pg-dev"
USER = "app-run-dev@{{ cookiecutter.gcp_project_id }}.iam"


@pytest.fixture
def iam() -> Settings:
    """What the Cloud Run revision sets for `cloud-sql-iam`."""
    return Settings(
        environment="production",
        database_auth="cloud-sql-iam",
        cloud_sql_instance=INSTANCE,
        database_name="app",
        database_user=USER,
    )


class FakeConnector:
    """Stands in for `google.cloud.sql.connector.Connector`, recording what it is asked for and
    dialling the test database where the real one would dial the instance."""

    instances: ClassVar[list[FakeConnector]] = []

    def __init__(self, **kwargs: Any) -> None:
        self.kwargs = kwargs
        self.constructed_on = threading.get_ident()
        self.connects: list[tuple[tuple[Any, ...], dict[str, Any]]] = []
        self.closed = False
        FakeConnector.instances.append(self)

    async def connect_async(self, *args: Any, **kwargs: Any) -> Any:
        self.connects.append((args, kwargs))
        target: Settings = FakeConnector.target
        return await asyncpg.connect(
            host=target.postgres_host,
            port=target.postgres_port,
            user=target.postgres_user,
            password=target.postgres_password.get_secret_value(),
            database=target.postgres_db,
            statement_cache_size=kwargs["statement_cache_size"],
        )

    async def close_async(self) -> None:
        self.closed = True

    target: ClassVar[Settings]


@pytest.fixture
def connector(settings: Settings, monkeypatch: pytest.MonkeyPatch) -> Iterator[type[FakeConnector]]:
    FakeConnector.instances = []
    FakeConnector.target = settings
    monkeypatch.setattr(database, "Connector", FakeConnector)
    yield FakeConnector


async def test_every_connection_goes_through_the_connector_as_the_iam_user(
    connector: type[FakeConnector], iam: Settings
) -> None:
    """One connector per process, built on the first connection rather than at import, and asked
    for an IAM-authenticated connection every time — no password, no socket."""
    engine = database.create_engine(iam)
    assert connector.instances == [], "the connector waits for the first connection"
    try:
        assert await _ready(engine) == "ok"
        assert await _ready(engine) == "ok"
    finally:
        await engine.dispose()
        await database.dispose_engine()

    (made,) = connector.instances
    assert made.kwargs == {
        "loop": asyncio.get_running_loop(),
        "refresh_strategy": "lazy",
        "enable_iam_auth": True,
    }
    # Its constructor resolves Application Default Credentials, which can ask the metadata server.
    assert made.constructed_on != threading.get_ident(), "the connector was built on the loop"
    assert len(made.connects) == 2
    for args, kwargs in made.connects:
        assert args == (INSTANCE, "asyncpg")
        assert kwargs["user"] == USER
        assert kwargs["db"] == "app"
        assert kwargs["enable_iam_auth"] is True
        assert "password" not in kwargs
    assert made.closed, "dispose_engine closes the connector the engine was built with"
{%- endif %}
