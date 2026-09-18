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
{% endif -%}
from collections.abc import AsyncIterator, Iterator

import pytest
from httpx import ASGITransport, AsyncClient
{%- if cookiecutter.use_postgres == "yes" %}
from sqlalchemy import text
from sqlalchemy.ext.asyncio import AsyncSession, async_sessionmaker, create_async_engine
from sqlalchemy.pool import NullPool
{%- endif %}

from app.core.config import Settings, get_settings
{%- if cookiecutter.use_postgres == "yes" %}
from app.core.database import get_db_session
{%- endif %}
from app.main import create_app
{%- if cookiecutter.use_postgres == "yes" %}
from app.models import Base
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
async def client(settings: Settings, session: AsyncSession) -> AsyncIterator[AsyncClient]:
    """An HTTP client bound to this test's transaction.

    The override is on the session dependency, which is the single seam
    the project structure rules intend — patching internals would couple the tests to
    the wiring instead of the contract.
    """
    app = create_app(settings)

    async def _session_override() -> AsyncIterator[AsyncSession]:
        yield session

    app.dependency_overrides[get_db_session] = _session_override
    transport = ASGITransport(app=app)
    async with AsyncClient(transport=transport, base_url="http://test") as http_client:
        yield http_client
    app.dependency_overrides.clear()
{%- else %}

@pytest.fixture
async def client(settings: Settings) -> AsyncIterator[AsyncClient]:
    app = create_app(settings)
    transport = ASGITransport(app=app)
    async with AsyncClient(transport=transport, base_url="http://test") as http_client:
        yield http_client
{%- endif %}
