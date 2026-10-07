"""Every revision goes up, comes back down, and ends where the models say it should.

The rest of the suite builds its schema from ORM metadata and never runs a migration, so without
this file a `downgrade()` is code nobody has executed and a model changed without a revision is
drift nobody has seen. Ask Athena for the database and migrations rules: a rollback you cannot
execute is not a rollback plan.

It runs on a connection of its own, in a schema of its own, inside one transaction that is rolled
back at the end. Postgres DDL is transactional, so nothing it creates outlives the test — and it
never touches the database's real schema, whichever database the suite happens to be pointed at.
"""

from __future__ import annotations

import os
from pathlib import Path

from alembic import command
from alembic.config import Config
from sqlalchemy import Connection, inspect, text
from sqlalchemy.ext.asyncio import create_async_engine
from sqlalchemy.pool import NullPool

from app.core.config import Settings

ALEMBIC_DIR = Path(__file__).resolve().parents[1] / "alembic"


def _tables(connection: Connection, schema: str) -> set[str]:
    return set(inspect(connection).get_table_names(schema=schema))


def _walk(connection: Connection, schema: str) -> None:
    # No ini file, on purpose: `alembic.ini` would reconfigure logging for the whole test process.
    config = Config()
    config.set_main_option("script_location", str(ALEMBIC_DIR))
    config.attributes["connection"] = connection

    command.upgrade(config, "head")
    assert {"users", "refresh_sessions"} <= _tables(connection, schema)
    # Raises if the models and the migrated schema disagree: a model changed with no revision.
    command.check(config)

    command.downgrade(config, "-1")
    tables = _tables(connection, schema)
    assert "users" in tables
    assert "refresh_sessions" not in tables

    command.upgrade(config, "head")
    command.downgrade(config, "base")
    assert _tables(connection, schema) <= {"alembic_version"}


async def test_every_revision_upgrades_downgrades_and_matches_the_models(
    settings: Settings,
) -> None:
    schema = f"migrations_{os.environ.get('PYTEST_XDIST_WORKER', 'gw0')}"
    engine = create_async_engine(settings.database_url, poolclass=NullPool)
    try:
        async with engine.connect() as connection:
            transaction = await connection.begin()
            await connection.execute(text(f'CREATE SCHEMA "{schema}"'))
            await connection.execute(text(f'SET LOCAL search_path TO "{schema}"'))
            await connection.run_sync(_walk, schema)
            await transaction.rollback()
    finally:
        await engine.dispose()
