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
from app.models import Base

ALEMBIC_DIR = Path(__file__).resolve().parents[1] / "alembic"


def _tables(connection: Connection, schema: str) -> set[str]:
    return set(inspect(connection).get_table_names(schema=schema))


def _names_in_database(connection: Connection, schema: str, table: str) -> set[str]:
    inspector = inspect(connection)
    names = {inspector.get_pk_constraint(table, schema=schema)["name"]}
    names |= {fk["name"] for fk in inspector.get_foreign_keys(table, schema=schema)}
    names |= {ix["name"] for ix in inspector.get_indexes(table, schema=schema)}
    names |= {uq["name"] for uq in inspector.get_unique_constraints(table, schema=schema)}
    names |= {ck["name"] for ck in inspector.get_check_constraints(table, schema=schema)}
    return names


def _names_in_models(table: str) -> set[str]:
    model = Base.metadata.tables[table]
    names = {str(constraint.name) for constraint in model.constraints}
    names |= {str(index.name) for index in model.indexes}
    return names


def _walk(connection: Connection, schema: str) -> None:
    # No ini file, on purpose: `alembic.ini` would reconfigure logging for the whole test process.
    config = Config()
    config.set_main_option("script_location", str(ALEMBIC_DIR))
    config.attributes["connection"] = connection

    command.upgrade(config, "head")
    assert {"users", "refresh_sessions"} <= _tables(connection, schema)
    # Raises if the models and the migrated schema disagree: a model changed with no revision.
    command.check(config)
    # `alembic check` matches primary and foreign keys by their columns, never by name, and a
    # `downgrade()` that drops one by a name the database never gave it fails only on the day it
    # is needed. Every index and constraint the migrations create carries the name the naming
    # convention on `Base.metadata` predicts.
    for table in Base.metadata.tables:
        assert _names_in_database(connection, schema, table) == _names_in_models(table), table

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
