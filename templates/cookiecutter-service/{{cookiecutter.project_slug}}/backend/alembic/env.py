"""Alembic environment. Async engine, metadata from app.models.

Two details here are load-bearing, and both fail silently when they are wrong:

1. `context.begin_transaction()` around `run_migrations()`. Without it the DDL executes and is then
   discarded when the connection closes — Alembic logs a successful upgrade over a database where
   nothing was created.
2. The URL is read from `config.attributes` first, and only then from Settings. Hard-coding it from
   Settings means a caller — the test suite, a one-off migration against a copy — cannot inject
   one, and there is no way to find that out except by reading this file.
"""

from __future__ import annotations

import asyncio
from logging.config import fileConfig

from alembic import context
from sqlalchemy import pool
from sqlalchemy.engine import Connection
from sqlalchemy.ext.asyncio import create_async_engine

from app.core.config import get_settings
from app.models import Base

config = context.config
if config.config_file_name is not None:
    fileConfig(config.config_file_name)

target_metadata = Base.metadata


def _database_url() -> str:
    """A caller-supplied URL wins over Settings."""
    injected = config.attributes.get("db_url")
    return str(injected) if injected else get_settings().database_url


def _run_migrations(connection: Connection) -> None:
    context.configure(
        connection=connection,
        target_metadata=target_metadata,
        compare_type=True,
        compare_server_default=True,
    )
    with context.begin_transaction():
        context.run_migrations()


def run_migrations_offline() -> None:
    context.configure(
        url=_database_url(),
        target_metadata=target_metadata,
        literal_binds=True,
        dialect_opts={"paramstyle": "named"},
    )
    with context.begin_transaction():
        context.run_migrations()


async def run_migrations_online() -> None:
    connectable = create_async_engine(_database_url(), poolclass=pool.NullPool)
    async with connectable.connect() as connection:
        await connection.run_sync(_run_migrations)
        await connection.commit()
    await connectable.dispose()


if context.is_offline_mode():
    run_migrations_offline()
else:
    asyncio.run(run_migrations_online())
