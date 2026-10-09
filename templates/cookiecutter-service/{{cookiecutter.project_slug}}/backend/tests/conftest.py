"""Test fixtures.
{% if cookiecutter.use_postgres == "yes" %}
Three decisions worth knowing, because they are what make `pytest -n auto` safe:

1. **A real Postgres, never SQLite.** A different dialect finds different bugs, and substituting
   one is banned outright. Ask Athena whether a library is approved.
2. **One schema per xdist worker.** Workers are separate processes; without structural isolation
   they race on the same tables and the failures look random. The schema is selected by setting
   asyncpg's `search_path` on the connection itself, so every statement in that worker lands in it
   with no per-query bookkeeping to forget.
3. **One transaction per test, rolled back.** `join_transaction_mode="create_savepoint"` means the
   application can call `commit()` — it closes a savepoint — and teardown still rolls the whole
   test back. That is what lets the real session dependency run unmodified.

The schema is built from ORM metadata rather than by running Alembic: it is faster, and it keeps
the suite independent of migration history. `alembic check` in CI is what guards the two from
drifting apart. Ask Athena for the backend testing rules.
{% else %}
There is no database yet, so the fixtures are the app factory and an HTTP client over it. When a
database arrives, this file grows per-worker schema isolation and per-test rollback — see the
handbook's backend testing page before inventing something else.
{% endif %}"""

from __future__ import annotations

{% if cookiecutter.use_postgres == "yes" -%}
import asyncio
import os
from collections.abc import AsyncIterator, Iterator
from contextlib import asynccontextmanager
{% else -%}
from collections.abc import AsyncIterator, Iterator
{% endif %}
import pytest
from httpx import ASGITransport, AsyncClient
{%- if cookiecutter.use_postgres == "yes" %}
from sqlalchemy import text
from sqlalchemy.ext.asyncio import AsyncSession, async_sessionmaker, create_async_engine
from sqlalchemy.pool import NullPool
{%- endif %}

from app.core.config import Settings, get_settings
{%- if cookiecutter.use_postgres == "yes" %}
from app.core.database import get_db_session, request_transaction
{%- endif %}
from app.main import create_app
{%- if cookiecutter.use_postgres == "yes" %}
from app.models import Base
from tests.helpers import ClientFactory
{%- endif %}


@pytest.fixture(autouse=True)
def _reset_settings_cache() -> Iterator[None]:
    """`get_settings` is `lru_cache`d. A test that changes the environment and does not clear it
    debugs a cached config for twenty minutes."""
    get_settings.cache_clear()
    yield
    get_settings.cache_clear()


@pytest.fixture(scope="session")
def settings() -> Settings:
    return Settings(environment="test")
{% if cookiecutter.use_postgres == "yes" %}

def _schema() -> str:
    """One schema per worker. `gw0` when running without xdist."""
    return f"test_{os.environ.get('PYTEST_XDIST_WORKER', 'gw0')}"


def _engine_for(settings: Settings, schema: str):  # type: ignore[no-untyped-def]
    """`server_settings` puts the search_path on the connection itself, so `create_all` and every
    query resolve into this worker's schema with no per-statement bookkeeping to forget."""
    return create_async_engine(
        settings.database_url,
        poolclass=NullPool,
        connect_args={"server_settings": {"search_path": schema}},
    )


async def _recreate_schema(settings: Settings, schema: str) -> None:
    engine = _engine_for(settings, schema)
    async with engine.begin() as connection:
        await connection.execute(text(f'DROP SCHEMA IF EXISTS "{schema}" CASCADE'))
        await connection.execute(text(f'CREATE SCHEMA "{schema}"'))
        await connection.run_sync(Base.metadata.create_all)
    await engine.dispose()


async def _drop_schema(settings: Settings, schema: str) -> None:
    engine = _engine_for(settings, schema)
    async with engine.begin() as connection:
        await connection.execute(text(f'DROP SCHEMA IF EXISTS "{schema}" CASCADE'))
    await engine.dispose()


@pytest.fixture(scope="session")
def _schema_ready(settings: Settings) -> Iterator[str]:
    """Deliberately a *sync* fixture driving `asyncio.run`.

    A session-scoped async fixture needs its event-loop scope widened to match, and getting that
    wrong produces a scope-mismatch error far from its cause. One-shot setup has no reason to share
    the test's loop, so it does not ask to.
    """
    schema = _schema()
    asyncio.run(_recreate_schema(settings, schema))
    yield schema
    asyncio.run(_drop_schema(settings, schema))


@pytest.fixture
async def session(settings: Settings, _schema_ready: str) -> AsyncIterator[AsyncSession]:
    """A session whose work is always rolled back.

    The outer transaction is never committed, so a test that commits closes a savepoint and still
    leaves the database untouched.
    """
    engine = create_async_engine(
        settings.database_url,
        poolclass=NullPool,
        connect_args={"server_settings": {"search_path": _schema_ready}},
    )
    connection = await engine.connect()
    transaction = await connection.begin()
    factory = async_sessionmaker(
        bind=connection,
        expire_on_commit=False,
        join_transaction_mode="create_savepoint",
    )

    async with factory() as db_session:
        yield db_session

    await transaction.rollback()
    await connection.close()
    await engine.dispose()


@pytest.fixture
def client_for(session: AsyncSession) -> ClientFactory:
    """HTTP clients over an app built from the given settings, bound to this test's transaction.

    The override is on the session dependency, which is the single seam the project structure
    rules intend — patching internals would couple the tests to the wiring instead of the contract.
    It wraps the session in the application's own `request_transaction`, so an error rolls a
    request's writes back here exactly as it does in production — to a savepoint, which teardown
    then discards with everything else — and the one error that keeps its writes keeps them.
    """

    @asynccontextmanager
    async def _client(
        app_settings: Settings, /, *, raise_app_exceptions: bool = True
    ) -> AsyncIterator[AsyncClient]:
        app = create_app(app_settings)

        async def _session_override() -> AsyncIterator[AsyncSession]:
            async with request_transaction(session):
                yield session

        app.dependency_overrides[get_db_session] = _session_override
        # A route reading `SettingsDep` sees the settings this app was built from, not whatever
        # `get_settings()` would load from the environment.
        app.dependency_overrides[get_settings] = lambda: app_settings
        # Raising by default, so an unexpected error fails the test with its own traceback rather
        # than as a status-code mismatch. Starlette re-raises an unhandled error after the
        # catch-all handler has sent its 500; `raise_app_exceptions=False` leaves the client
        # holding that 500 instead, which is what a real server's caller gets.
        transport = ASGITransport(app=app, raise_app_exceptions=raise_app_exceptions)
        async with AsyncClient(transport=transport, base_url="http://test") as http_client:
            yield http_client
        app.dependency_overrides.clear()

    return _client


@pytest.fixture
async def client(settings: Settings, client_for: ClientFactory) -> AsyncIterator[AsyncClient]:
    """An HTTP client over the default test app, bound to this test's transaction."""
    async with client_for(settings) as http_client:
        yield http_client
{%- else %}

@pytest.fixture
async def client(settings: Settings) -> AsyncIterator[AsyncClient]:
    app = create_app(settings)
    transport = ASGITransport(app=app)
    async with AsyncClient(transport=transport, base_url="http://test") as http_client:
        yield http_client
{%- endif %}
