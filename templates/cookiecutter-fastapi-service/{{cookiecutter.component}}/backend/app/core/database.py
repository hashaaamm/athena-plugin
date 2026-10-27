"""Engine, session factory and the session lifecycle.

Nothing commits except the session dependency. Repositories `flush()`.
Ask Athena for the layered architecture rules; AGENTS.md rule 5 says the same thing.

How the engine authenticates is `DATABASE_AUTH`, and it is decided here and nowhere else: a
repository, a service and the readiness probe get the same session whichever mode built it.
{%- if cookiecutter.deploy_target == "aws-ecs-express" %}
`rds-iam` signs a token with the task role's credentials for every new connection; `password` is
everything else — local development, the test suite and the migration task.
{%- else %}
`cloud-sql-iam` dials through the Cloud SQL connector as the revision's service account;
`password` is everything else — local development, the test suite and the migration job.
{%- endif %}
"""

from __future__ import annotations

import asyncio
from collections.abc import AsyncIterator{% if cookiecutter.deploy_target == "aws-ecs-express" %}, Callable{% endif %}
from contextlib import asynccontextmanager
{%- if cookiecutter.deploy_target == "aws-ecs-express" %}
from functools import cache
{%- endif %}
from typing import Any

{% if cookiecutter.deploy_target == "aws-ecs-express" -%}
import boto3
{% endif -%}
{% if cookiecutter.deploy_target == "gcp-cloud-run" -%}
from google.cloud.sql.connector import Connector
{% endif -%}
{% if cookiecutter.deploy_target == "aws-ecs-express" -%}
from sqlalchemy import event
{% endif -%}
from sqlalchemy.ext.asyncio import (
    AsyncEngine,
    AsyncSession,
    async_sessionmaker,
    create_async_engine,
)
from sqlalchemy.pool import NullPool

from app.core.config import Settings, get_settings
from app.core.exceptions import AppError

{%- if cookiecutter.deploy_target == "aws-ecs-express" %}

#: Produces the password for one new connection. Called on a worker thread, never on the loop.
PasswordProvider = Callable[[], str]
{%- endif %}


def create_engine(settings: Settings | None = None) -> AsyncEngine:
    """Build the async engine.

    `NullPool` and a disabled prepared-statement cache are deliberate, and specific to running on
    a platform that scales instances up and down under us in front of a managed database: a pool
    held across that boundary hands out sockets the other end has already closed, and asyncpg's
    named prepared statements break outright when a pooler multiplexes sessions onto shared server
    connections.

    The database access and migrations rules MUST both settings behind transaction pooling.

    `NullPool` is also the connection lifetime an IAM mode wants. A token is checked only when a
    connection opens, so a long-lived connection would keep working after its identity's grant
    was revoked; here no connection outlives the session that opened it, which is well under the
    fifteen minutes a token is valid for. A pool that keeps connections — a `QueuePool` — needs
    `pool_recycle` under 900 seconds for the same reason, and `NullPool` does not support one.
    """
    settings = settings or get_settings()
{%- if cookiecutter.deploy_target == "gcp-cloud-run" %}
    if settings.database_auth == "cloud-sql-iam":
        return _cloud_sql_iam_engine(settings)
{%- endif %}
    engine = create_async_engine(
        settings.database_url,
        poolclass=NullPool,
        connect_args={"prepared_statement_cache_size": 0, "statement_cache_size": 0},
        echo=False,
    )
{%- if cookiecutter.deploy_target == "aws-ecs-express" %}
    if settings.database_auth == "rds-iam":
        password_per_connection(engine, rds_iam_token_provider(settings))
{%- endif %}
    return engine

{%- if cookiecutter.deploy_target == "aws-ecs-express" %}


def password_per_connection(engine: AsyncEngine, provider: PasswordProvider) -> None:
    """Give every new connection a password `provider` makes for it, at the moment it connects.

    The `do_connect` event runs before the driver is called, on the event loop, and must not block
    it. So it does not call `provider`: it hands asyncpg an async callable as the password, which
    asyncpg awaits as it connects, and that callable runs `provider` on a worker thread. A
    provider may be cheap — signing an RDS token is local HMAC work — and still block, because
    the credentials it signs with are refreshed over HTTP when they near expiry, and blocking I/O
    leaves the loop (ADR-0015).
    """

    async def _password() -> str:
        return await asyncio.to_thread(provider)

    def _inject(_dialect: Any, _record: Any, _cargs: list[Any], cparams: dict[str, Any]) -> None:
        cparams["password"] = _password

    event.listen(engine.sync_engine, "do_connect", _inject)


def rds_iam_token_provider(settings: Settings) -> PasswordProvider:
    """An RDS IAM authentication token for `DATABASE_USER`, signed with the default credentials.

    On ECS the default chain finds the task role. The token is a SigV4 presigned URL: computed
    locally, valid for fifteen minutes, and checked by RDS only when a connection opens — which is
    why a fresh one is made for every connection rather than cached.
    """

    @cache
    def _client() -> Any:
        # Built once, on the first connection's worker thread rather than at import: creating a
        # client reads the service model from disk.
        return boto3.client("rds", region_name=settings.aws_region)

    def _token() -> str:
        token: str = _client().generate_db_auth_token(
            DBHostname=settings.database_host,
            Port=settings.database_tcp_port,
            DBUsername=settings.database_user,
            Region=settings.aws_region,
        )
        return token

    return _token
{%- endif %}
{%- if cookiecutter.deploy_target == "gcp-cloud-run" %}


class CloudSqlIamConnect:
    """The engine's `async_creator` for `cloud-sql-iam`: one connection through the connector.

    The connector fetches an ephemeral certificate for the instance, opens TLS to it and logs in as
    the revision's service account with a fresh OAuth token — no password, no Unix socket, no
    Cloud SQL volume on the service. It is created on the first connection rather than at import,
    because it has to be bound to the running event loop (asyncpg connections cannot cross loops),
    and its constructor resolves Application Default Credentials, which can ask the metadata
    server: blocking I/O, so that part runs on a worker thread.

    `lazy` refresh, because Cloud Run throttles CPU outside requests (`cpu_idle`) and a background
    refresh would be starved; lazy refreshes the certificate when a connection needs it.
    """

    def __init__(self, settings: Settings) -> None:
        self._instance = settings.cloud_sql_instance
        self._user = settings.database_user
        self._database = settings.database_name
        self._connector: Connector | None = None
        self._lock = asyncio.Lock()

    async def __call__(self) -> Any:
        """An `asyncpg.Connection`. Typed `Any` because asyncpg ships no type information."""
        connector = await self._connector_for_this_loop()
        return await connector.connect_async(
            self._instance,
            "asyncpg",
            user=self._user,
            db=self._database,
            enable_iam_auth=True,
            # asyncpg's own statement cache, off for the reason in `create_engine`. SQLAlchemy's
            # adapter cache cannot be set through `async_creator`, and does not need to be: no
            # pooler sits between the connector and the instance to multiplex sessions.
            statement_cache_size=0,
        )

    async def _connector_for_this_loop(self) -> Connector:
        async with self._lock:
            if self._connector is None:
                loop = asyncio.get_running_loop()
                self._connector = await asyncio.to_thread(
                    Connector, loop=loop, refresh_strategy="lazy", enable_iam_auth=True
                )
            return self._connector

    async def close(self) -> None:
        if self._connector is not None:
            await self._connector.close_async()
            self._connector = None


#: The connector behind the process's engine, closed with it.
_cloud_sql: CloudSqlIamConnect | None = None


def _cloud_sql_iam_engine(settings: Settings) -> AsyncEngine:
    global _cloud_sql
    _cloud_sql = CloudSqlIamConnect(settings)
    return create_async_engine(
        settings.database_url,
        poolclass=NullPool,
        async_creator=_cloud_sql,
        echo=False,
    )
{%- endif %}


_engine: AsyncEngine | None = None
_session_factory: async_sessionmaker[AsyncSession] | None = None


def get_engine() -> AsyncEngine:
    global _engine
    if _engine is None:
        _engine = create_engine()
    return _engine


def get_session_factory() -> async_sessionmaker[AsyncSession]:
    global _session_factory
    if _session_factory is None:
        _session_factory = async_sessionmaker(
            bind=get_engine(),
            expire_on_commit=False,
            autoflush=False,
        )
    return _session_factory


async def dispose_engine() -> None:
    """Release connections on shutdown. Called from the app lifespan.

    Without it, every redeploy leaks connections until Postgres refuses new ones.
    """
    global _engine, _session_factory{% if cookiecutter.deploy_target == "gcp-cloud-run" %}, _cloud_sql{% endif %}
    if _engine is not None:
        await _engine.dispose()
{%- if cookiecutter.deploy_target == "gcp-cloud-run" %}
    if _cloud_sql is not None:
        # The connector holds an HTTP session and refresh state of its own; disposing the engine
        # does not reach it.
        await _cloud_sql.close()
    _cloud_sql = None
{%- endif %}
    _engine = None
    _session_factory = None


@asynccontextmanager
async def request_transaction(session: AsyncSession) -> AsyncIterator[AsyncSession]:
    """Commit or roll back one request's work. The only code in the service that decides which.

    Success commits. An error rolls back, so a failure late in a use case undoes the writes that
    came earlier in it — except an `AppError` that declares `keeps_writes`, whose writes are the
    outcome the error reports. FastAPI throws a route's exception into the dependency that yielded
    the session, which is what lets this see it at all.

    Separate from `get_db_session` so the test suite can wrap its own session in the same decision
    rather than a copy of it.
    """
    try:
        yield session
    except AppError as exc:
        if exc.keeps_writes:
            await session.commit()
        else:
            await session.rollback()
        raise
    except Exception:
        await session.rollback()
        raise
    else:
        await session.commit()


async def get_db_session() -> AsyncIterator[AsyncSession]:
    """One transaction per request. Nothing else commits.

    Commit lives here rather than in a repository so the use case is one unit of work, and so the
    test harness can wrap each test in an outer transaction it rolls back.

    The commit runs before the response is sent only because `SessionDep` in `app/api/deps.py`
    declares `scope="function"`. Depending on this function any other way puts the commit after
    the response, where a failure can no longer change the status code the client was given.
    """
    async with get_session_factory()() as session, request_transaction(session):
        yield session
