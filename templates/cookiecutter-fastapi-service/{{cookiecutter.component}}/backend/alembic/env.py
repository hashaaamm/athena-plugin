"""Alembic environment. Async engine, metadata from app.models.

Two details here are load-bearing, and both fail silently when they are wrong:

1. `context.begin_transaction()` around `run_migrations()`. Without it the DDL executes and is then
   discarded when the connection closes — Alembic logs a successful upgrade over a database where
   nothing was created.
2. The URL is read from `config.attributes` first, and only then from Settings. Hard-coding it from
   Settings means a caller — the test suite, a one-off migration against a copy — cannot inject
   one, and there is no way to find that out except by reading this file.

A caller may also hand over an open connection as `config.attributes["connection"]`, and then the
migrations run on it, inside whatever transaction it already holds. That is how
`tests/test_migrations.py` runs every revision up and down in a schema of its own and rolls the
lot back — the suite never touches a database it did not create.

**The deployed service is not the role that runs this.** The migration connects as the database's
admin login and owns the schema; the service connects as `DATABASE_APP_ROLE` with IAM
authentication (`DATABASE_APP_AUTH` says which kind) and owns nothing. So every run ends by granting
that role what the service needs — see `_grant_app_role` — in the same transaction as the
migrations, after them, so the tables they just created are covered.
"""

from __future__ import annotations

import asyncio
from logging.config import fileConfig

from alembic import context
from sqlalchemy import pool
from sqlalchemy.engine import Connection
from sqlalchemy.ext.asyncio import AsyncEngine, create_async_engine

from app.core.config import APP_ROLE_PATTERN, AppAuth, get_settings
from app.core.database import create_engine
from app.models import Base

config = context.config
if config.config_file_name is not None:
    fileConfig(config.config_file_name)

target_metadata = Base.metadata


def _database_url() -> str:
    """A caller-supplied URL wins over Settings."""
    injected = config.attributes.get("db_url")
    return str(injected) if injected else get_settings().database_url


def _engine() -> AsyncEngine:
    """The application's own engine, so a migration authenticates exactly as `DATABASE_AUTH` says
    — unless a caller injected a URL, which is used as it is."""
    injected = config.attributes.get("db_url")
    if injected:
        return create_async_engine(str(injected), poolclass=pool.NullPool)
    return create_engine(get_settings())


def _app_role() -> tuple[str, AppAuth | None]:
    """The role to grant and how it logs in. A caller-supplied pair wins over Settings, as the URL
    does; Settings has already refused one without the other."""
    if "app_role" in config.attributes:
        role, auth = str(config.attributes["app_role"]), config.attributes.get("app_auth")
    else:
        settings = get_settings()
        role, auth = settings.database_app_role, settings.database_app_auth
    if role and not APP_ROLE_PATTERN.fullmatch(role):
        raise ValueError(f"app role {role!r} must be 1-63 letters, digits or _ . @ -")
    if role and auth not in ("rds-iam", "cloud-sql-iam"):
        raise ValueError(f"app role {role!r} needs an auth of rds-iam or cloud-sql-iam, not {auth!r}")
    return role, auth


def _grant_app_role(role: str, auth: AppAuth | None) -> None:
    """Give the service's role what it needs and nothing more, idempotently, on every run.

    `USAGE` on the schema; `SELECT, INSERT, UPDATE, DELETE` on every table and `USAGE, SELECT` on
    every sequence in it; and the same as default privileges, so a table the next migration
    creates is covered before anything has to remember to grant it. No `CREATE`, no `TRUNCATE`, no
    ownership: a compromised service can change rows, not the schema.

    The schema is `current_schema()` — `public` in every deployed database, and whichever schema a
    test's `search_path` names. One `DO` block rather than separate statements, so it resolves that
    at run time and renders unchanged into `alembic upgrade --sql`. `format('%I')` quotes every
    identifier; the role name is the one literal, and `APP_ROLE_PATTERN` admits nothing that could
    end it.
    """
    literal = role.replace("'", "''")
    if auth == "rds-iam":
        # RDS: the role does not exist until this creates it, and `rds_iam` membership is what
        # makes RDS accept an IAM token for it in place of a password. It has no password at all.
        create = """
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = app_role) THEN
    EXECUTE format('CREATE ROLE %I WITH LOGIN', app_role);
  END IF;
  EXECUTE format('GRANT rds_iam TO %I', app_role);"""
    else:
        # Cloud SQL: the stack creates the role, as the `CLOUD_IAM_SERVICE_ACCOUNT` user named for
        # the runtime service account. Creating it here instead would make an ordinary role IAM
        # cannot log in as, so a missing one fails the migration, by name, rather than being
        # papered over.
        create = """
  IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = app_role) THEN
    RAISE EXCEPTION 'role % does not exist: Cloud SQL creates it from the stack''s IAM user',
      app_role;
  END IF;"""
    context.execute(
        f"""
DO $$
DECLARE
  app_role text := '{literal}';
  target_schema text := current_schema();
BEGIN{create}
  EXECUTE format('GRANT USAGE ON SCHEMA %I TO %I',
    target_schema, app_role);
  EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA %I TO %I',
    target_schema, app_role);
  EXECUTE format('GRANT USAGE, SELECT ON ALL SEQUENCES IN SCHEMA %I TO %I',
    target_schema, app_role);
  EXECUTE format('ALTER DEFAULT PRIVILEGES IN SCHEMA %I '
    'GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO %I',
    target_schema, app_role);
  EXECUTE format('ALTER DEFAULT PRIVILEGES IN SCHEMA %I '
    'GRANT USAGE, SELECT ON SEQUENCES TO %I',
    target_schema, app_role);
END
$$"""
    )


def _run_migrations(connection: Connection) -> None:
    context.configure(
        connection=connection,
        target_metadata=target_metadata,
        compare_type=True,
        compare_server_default=True,
    )
    with context.begin_transaction():
        context.run_migrations()
        role, auth = _app_role()
        if role:
            _grant_app_role(role, auth)


def run_migrations_offline() -> None:
    context.configure(
        url=_database_url(),
        target_metadata=target_metadata,
        literal_binds=True,
        dialect_opts={"paramstyle": "named"},
    )
    with context.begin_transaction():
        context.run_migrations()
        role, auth = _app_role()
        if role:
            _grant_app_role(role, auth)


async def run_migrations_online() -> None:
    connectable = _engine()
    async with connectable.connect() as connection:
        await connection.run_sync(_run_migrations)
        await connection.commit()
    await connectable.dispose()


if context.is_offline_mode():
    run_migrations_offline()
elif isinstance(shared := config.attributes.get("connection"), Connection):
    # Already inside the caller's event loop and transaction: no engine, no `asyncio.run`.
    _run_migrations(shared)
else:
    asyncio.run(run_migrations_online())
