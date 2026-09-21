"""create users

Revision ID: 0001
Revises:
Create Date: generated with the template

The only table the template ships, and the root of this project's history. Every later revision
hangs off it, so `alembic revision --autogenerate` writes a child rather than a second head.

Do not delete it to "start clean". A revision that has run against a database somewhere is part of
that database's history; dropping it from the repository makes `alembic current` point at a
revision the code no longer contains.
"""

from __future__ import annotations

from collections.abc import Sequence

import sqlalchemy as sa
from alembic import op
from sqlalchemy.dialects import postgresql

revision: str = "0001"
down_revision: str | None = None
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    # A blocked ALTER queues behind a long read *and* blocks every query behind it. Bounding the
    # wait turns that from an outage into a failed migration you can retry.
    op.execute("SET lock_timeout = '3s'")
    op.create_table(
        "users",
        sa.Column(
            "id",
            postgresql.UUID(as_uuid=True),
            server_default=sa.text("gen_random_uuid()"),
            nullable=False,
        ),
        sa.Column("email", sa.String(length=320), nullable=False),
        # Wide enough for an Argon2id digest with room for a parameter change. See app/models/user.py.
        sa.Column("hashed_password", sa.String(length=255), nullable=False),
        sa.Column("is_active", sa.Boolean(), server_default=sa.text("true"), nullable=False),
        sa.Column(
            "created_at", sa.DateTime(timezone=True), server_default=sa.text("now()"), nullable=False
        ),
        sa.Column(
            "updated_at", sa.DateTime(timezone=True), server_default=sa.text("now()"), nullable=False
        ),
        sa.PrimaryKeyConstraint("id"),
    )
    # Unique *index*, matching `unique=True, index=True` on the model. Emitting a separate
    # UniqueConstraint as well would make `alembic check` report permanent drift.
    op.create_index("ix_users_email", "users", ["email"], unique=True)


def downgrade() -> None:
    op.drop_index("ix_users_email", table_name="users")
    op.drop_table("users")
