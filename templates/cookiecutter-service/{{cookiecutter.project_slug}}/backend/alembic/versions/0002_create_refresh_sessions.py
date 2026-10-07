"""create refresh_sessions

Revision ID: 0002
Revises: 0001
Create Date: generated with the template

One row per refresh token issued, keyed by the token's `jti`. The token itself is not stored. See
app/models/refresh_session.py for what each column is for.
"""

from __future__ import annotations

from collections.abc import Sequence

import sqlalchemy as sa
from alembic import op
from sqlalchemy.dialects import postgresql

revision: str = "0002"
down_revision: str | None = "0001"
branch_labels: str | Sequence[str] | None = None
depends_on: str | Sequence[str] | None = None


def upgrade() -> None:
    # A blocked statement queues behind a long read *and* blocks every query behind it. Bounding the
    # wait turns that from an outage into a failed migration you can retry. The foreign key below
    # takes a lock on `users`, which is the table this matters for.
    op.execute("SET lock_timeout = '3s'")
    op.create_table(
        "refresh_sessions",
        # No server default: the id is the refresh token's `jti`, and the service always sets it.
        sa.Column("id", postgresql.UUID(as_uuid=True), nullable=False),
        sa.Column("user_id", postgresql.UUID(as_uuid=True), nullable=False),
        sa.Column("family_id", postgresql.UUID(as_uuid=True), nullable=False),
        sa.Column("expires_at", sa.DateTime(timezone=True), nullable=False),
        sa.Column("revoked_at", sa.DateTime(timezone=True), nullable=True),
        sa.Column(
            "created_at", sa.DateTime(timezone=True), server_default=sa.text("now()"), nullable=False
        ),
        sa.Column(
            "updated_at", sa.DateTime(timezone=True), server_default=sa.text("now()"), nullable=False
        ),
        sa.ForeignKeyConstraint(["user_id"], ["users.id"], ondelete="CASCADE"),
        sa.PrimaryKeyConstraint("id"),
    )
    # Plain, not CONCURRENTLY. Both are built on a table this same transaction has just created, so
    # nothing else can be reading or writing it, and CREATE INDEX CONCURRENTLY cannot run inside a
    # transaction at all. An index added later to a table with traffic is the case that needs it.
    op.create_index("ix_refresh_sessions_family_id", "refresh_sessions", ["family_id"])
    op.create_index("ix_refresh_sessions_user_id", "refresh_sessions", ["user_id"])


def downgrade() -> None:
    # Every refresh token in circulation stops working, because the rows that vouch for them are
    # gone. Access tokens are unaffected and expire on their own; users sign in again.
    op.drop_index("ix_refresh_sessions_user_id", table_name="refresh_sessions")
    op.drop_index("ix_refresh_sessions_family_id", table_name="refresh_sessions")
    op.drop_table("refresh_sessions")
