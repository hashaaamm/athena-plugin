"""One refresh token this service issued, and whether it may still be spent.

A JWT is valid because it verifies, not because anything says it still should be. That is what
makes the access token cheap, and it is what would make a thirty-day refresh token a thirty-day
credential nobody could take back. This table buys revocation back for the refresh token only, at
one database round trip per refresh; the access token is still checked against nothing.

**The token itself is never stored**, only its `jti`. A dump of this table gives an attacker a list
of session identifiers and no credentials, which is the difference between an incident and a
breach.

**Revocation is per family, not per token.** Every token descended from one sign-in shares a
`family_id`. Rotating one revokes its row and inserts the next; a revoked row presented again means
the token was replayed or stolen, and the whole family goes — see `AuthService.refresh`.

Rows are never deleted on the request path. A device that refreshes every fifteen minutes writes
about 2,900 rows a month, so a project in production wants a scheduled job deleting rows some days
past `expires_at`; nothing in the template runs one.
"""

from __future__ import annotations

import datetime as dt
import uuid

from sqlalchemy import DateTime, ForeignKey
from sqlalchemy.dialects.postgresql import UUID as PG_UUID
from sqlalchemy.orm import Mapped, mapped_column

from app.models.base import Base, TimestampMixin


class RefreshSession(Base, TimestampMixin):
    __tablename__ = "refresh_sessions"

    #: The refresh token's own `jti`, so a row is found from the token without storing the token.
    #: No server default, unlike `UUIDMixin`: an id this table invented would name no token.
    id: Mapped[uuid.UUID] = mapped_column(PG_UUID(as_uuid=True), primary_key=True)
    #: `CASCADE`, so deleting a user takes their sessions with them rather than failing on them.
    user_id: Mapped[uuid.UUID] = mapped_column(
        PG_UUID(as_uuid=True), ForeignKey("users.id", ondelete="CASCADE"), index=True
    )
    #: Shared by every token descended from one sign-in. Indexed because revoking a family is an
    #: UPDATE by this column, and it runs on the path where a stolen token was just detected.
    family_id: Mapped[uuid.UUID] = mapped_column(PG_UUID(as_uuid=True), nullable=False, index=True)
    #: The token's `exp`. Checked here as well as in the token, so the row is the authority.
    expires_at: Mapped[dt.datetime] = mapped_column(DateTime(timezone=True), nullable=False)
    #: Set when the token is rotated, logged out or revoked with its family. Null means spendable.
    revoked_at: Mapped[dt.datetime | None] = mapped_column(DateTime(timezone=True), nullable=True)
