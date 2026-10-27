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
from collections.abc import Callable
from pathlib import Path

import pytest
from alembic import command
from alembic.config import Config
from sqlalchemy import Connection, inspect, text
from sqlalchemy.exc import DBAPIError
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


# --- The service's role -----------------------------------------------------
#
# Deployed, the migration runs as the admin login and the service connects as a role of its own,
# with IAM authentication and no password. Every upgrade ends by granting that role what the service
# needs; these run it against a stand-in role, in a schema and a transaction that are rolled back.
# Roles are cluster-wide rather than per database, so each stand-in is named for the xdist worker.
# Both kinds are plain SQL, so both are tested whichever target this project deploys to.


def _privileges(connection: Connection, role: str, relation: str) -> set[str]:
    wanted = ("SELECT", "INSERT", "UPDATE", "DELETE", "TRUNCATE", "REFERENCES", "TRIGGER")
    return {
        privilege
        for privilege in wanted
        if connection.execute(
            text("SELECT has_table_privilege(:role, :relation, :privilege)"),
            {"role": role, "relation": relation, "privilege": privilege},
        ).scalar_one()
    }


def _upgrade_granting(connection: Connection, role: str, auth: str) -> None:
    config = Config()
    config.set_main_option("script_location", str(ALEMBIC_DIR))
    config.attributes["connection"] = connection
    config.attributes["app_role"] = role
    config.attributes["app_auth"] = auth
    command.upgrade(config, "head")


def _grant_walk(connection: Connection, schema: str, role: str, auth: str) -> None:
    _upgrade_granting(connection, role, auth)
    # Twice: it runs on every deploy, so it must be a no-op the second time rather than an error.
    _upgrade_granting(connection, role, auth)

    def schema_privilege(privilege: str) -> bool:
        return bool(
            connection.execute(
                text("SELECT has_schema_privilege(:role, :schema, :privilege)"),
                {"role": role, "schema": schema, "privilege": privilege},
            ).scalar_one()
        )

    assert schema_privilege("USAGE")
    assert not schema_privilege("CREATE"), "the service may change rows, never the schema"
    for table in ("users", "refresh_sessions"):
        assert _privileges(connection, role, f'"{schema}".{table}') == {
            "SELECT",
            "INSERT",
            "UPDATE",
            "DELETE",
        }, table

    # Default privileges: a table the *next* migration creates is covered without a new grant.
    connection.execute(text(f'CREATE TABLE "{schema}".created_later (id serial PRIMARY KEY)'))
    assert "SELECT" in _privileges(connection, role, f'"{schema}".created_later')
    sequence_usage = connection.execute(
        text("SELECT has_sequence_privilege(:role, :sequence, 'USAGE')"),
        {"role": role, "sequence": f'"{schema}".created_later_id_seq'},
    ).scalar_one()
    assert sequence_usage, "an insert into a serial column needs the sequence"


async def _in_a_rolled_back_schema(
    settings: Settings, schema: str, setup: list[str], walk: Callable[[Connection], None]
) -> None:
    engine = create_async_engine(settings.database_url, poolclass=NullPool)
    try:
        async with engine.connect() as connection:
            transaction = await connection.begin()
            for statement in setup:
                await connection.execute(text(statement))
            await connection.execute(text(f'CREATE SCHEMA "{schema}"'))
            await connection.execute(text(f'SET LOCAL search_path TO "{schema}"'))
            await connection.run_sync(walk)
            await transaction.rollback()
    finally:
        await engine.dispose()


async def test_rds_iam_creates_the_service_role_and_grants_it_the_tables(
    settings: Settings,
) -> None:
    """On RDS the role does not exist until the first migration creates it — with a login, no
    password, and membership in `rds_iam`, which is what makes RDS accept a token for it."""
    worker = os.environ.get("PYTEST_XDIST_WORKER", "gw0")
    schema, role = f"grants_rds_{worker}", f"app_iam_{worker}"

    def walk(connection: Connection) -> None:
        _grant_walk(connection, schema, role, "rds-iam")
        login, iam = connection.execute(
            text(
                "SELECT rolcanlogin, pg_has_role(:role, 'rds_iam', 'MEMBER') "
                "FROM pg_roles WHERE rolname = :role"
            ),
            {"role": role},
        ).one()
        assert login, "the service logs in as this role"
        assert iam, "membership in rds_iam is what makes RDS accept a token in place of a password"

    # RDS has `rds_iam` built in. A plain Postgres needs a stand-in, created and rolled back with
    # everything else.
    stand_in = (
        "DO $$ BEGIN IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'rds_iam') "
        "THEN CREATE ROLE rds_iam; END IF; END $$"
    )
    await _in_a_rolled_back_schema(settings, schema, [stand_in], walk)


async def test_cloud_sql_iam_grants_the_iam_user_the_tables(settings: Settings) -> None:
    """On Cloud SQL the stack creates the role, as the IAM user named for the runtime service
    account — `name@project.iam`, which only survives the grant script quoted."""
    worker = os.environ.get("PYTEST_XDIST_WORKER", "gw0")
    schema, role = f"grants_sql_{worker}", f"svc-run-{worker}@test-project.iam"
    await _in_a_rolled_back_schema(
        settings,
        schema,
        [f'CREATE ROLE "{role}"'],
        lambda connection: _grant_walk(connection, schema, role, "cloud-sql-iam"),
    )


async def test_cloud_sql_iam_refuses_a_missing_user_rather_than_creating_one(
    settings: Settings,
) -> None:
    """Creating it here would make an ordinary role IAM cannot log in as, and a service that
    cannot connect for a reason nobody can see. The migration fails instead, naming the role."""
    worker = os.environ.get("PYTEST_XDIST_WORKER", "gw0")
    role = f"nobody-{worker}@test-project.iam"

    def walk(connection: Connection) -> None:
        with pytest.raises(DBAPIError, match="does not exist"):
            _upgrade_granting(connection, role, "cloud-sql-iam")

    await _in_a_rolled_back_schema(settings, f"missing_{worker}", [], walk)
