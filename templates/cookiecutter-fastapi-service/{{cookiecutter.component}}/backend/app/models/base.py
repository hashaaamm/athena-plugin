"""Declarative base and the mixins every table gets.

Repeating `created_at`/`updated_at` per model is how they drift.
"""

from __future__ import annotations

import datetime as dt
import uuid

from sqlalchemy import DateTime, MetaData, func
from sqlalchemy.dialects.postgresql import UUID as PG_UUID
from sqlalchemy.orm import DeclarativeBase, Mapped, mapped_column

#: Every index and constraint gets a name the code can predict. Left unnamed, Postgres invents one
#: — `users_pkey`, `refresh_sessions_user_id_fkey` — that no file in this repository contains, and
#: `drop_constraint` needs that name in a `downgrade()` and in every later change to the
#: constraint. Autogenerate writes these names into each revision, so a migration and the models
#: agree on them by construction. `tests/test_migrations.py` checks that they do.
NAMING_CONVENTION = {
    "ix": "ix_%(column_0_label)s",
    "uq": "uq_%(table_name)s_%(column_0_name)s",
    "ck": "ck_%(table_name)s_%(constraint_name)s",
    "fk": "fk_%(table_name)s_%(column_0_name)s_%(referred_table_name)s",
    "pk": "pk_%(table_name)s",
}


class Base(DeclarativeBase):
    """Every model inherits this. Alembic autogenerate reads its metadata."""

    metadata = MetaData(naming_convention=NAMING_CONVENTION)


class UUIDMixin:
    # Generated server-side so a row inserted by a migration or by psql gets an id too.
    id: Mapped[uuid.UUID] = mapped_column(
        PG_UUID(as_uuid=True),
        primary_key=True,
        server_default=func.gen_random_uuid(),
    )


class TimestampMixin:
    created_at: Mapped[dt.datetime] = mapped_column(
        DateTime(timezone=True), server_default=func.now(), nullable=False
    )
    updated_at: Mapped[dt.datetime] = mapped_column(
        DateTime(timezone=True),
        server_default=func.now(),
        onupdate=func.now(),
        nullable=False,
    )
