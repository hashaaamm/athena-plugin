"""Engine, session factory and the session lifecycle.

Nothing commits except the session dependency. Repositories `flush()`.
Ask Athena for the layered architecture rules; AGENTS.md rule 5 says the same thing.
"""

from __future__ import annotations

from collections.abc import AsyncIterator

from sqlalchemy.ext.asyncio import (
    AsyncEngine,
    AsyncSession,
    async_sessionmaker,
    create_async_engine,
)
from sqlalchemy.pool import NullPool

from app.core.config import Settings, get_settings


def create_engine(settings: Settings | None = None) -> AsyncEngine:
    """Build the async engine.

    `NullPool` and a disabled prepared-statement cache are deliberate, and specific to running on
    Cloud Run in front of Cloud SQL. The platform scales instances up and down under us and the
    database sits behind a connection proxy: a pool held across that boundary hands out sockets the
    other end has already closed, and asyncpg's named prepared statements break outright when a
    pooler multiplexes sessions onto shared server connections.

    The database access and migrations rules MUST both settings behind transaction pooling.
    """
    settings = settings or get_settings()
    return create_async_engine(
        settings.database_url,
        poolclass=NullPool,
        connect_args={"prepared_statement_cache_size": 0, "statement_cache_size": 0},
        echo=False,
    )


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
    global _engine, _session_factory
    if _engine is not None:
        await _engine.dispose()
    _engine = None
    _session_factory = None


async def get_db_session() -> AsyncIterator[AsyncSession]:
    """One transaction per request. The only place that commits.

    Commit lives here rather than in a repository so a failure late in a use case undoes the
    writes that came earlier in it, and so the test harness can wrap each test in an outer
    transaction it rolls back.
    """
    async with get_session_factory()() as session:
        try:
            yield session
        except Exception:
            await session.rollback()
            raise
        else:
            await session.commit()
