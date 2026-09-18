"""create items

Revision ID: 0001
Revises:
Create Date: generated with the template

The example resource's table. Delete it along with the example resource once you have a real one.
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
        "items",
        sa.Column(
            "id",
            postgresql.UUID(as_uuid=True),
            server_default=sa.text("gen_random_uuid()"),
            nullable=False,
        ),
        sa.Column("name", sa.String(length=200), nullable=False),
        sa.Column("description", sa.Text(), nullable=True),
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
    op.create_index("ix_items_name", "items", ["name"], unique=True)


def downgrade() -> None:
    op.drop_index("ix_items_name", table_name="items")
    op.drop_table("items")
