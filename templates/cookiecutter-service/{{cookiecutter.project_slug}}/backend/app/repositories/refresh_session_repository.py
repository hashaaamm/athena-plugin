"""All RefreshSession data access.

Rows and locks, nothing else. Whether a presented token is a replay, and what to do about it, is
`AuthService.refresh`'s decision; this layer only knows how to find a row and mark rows revoked.
"""

from __future__ import annotations

import datetime as dt
import uuid

from sqlalchemy import select, update

from app.models.refresh_session import RefreshSession
from app.repositories.base import BaseRepository


class RefreshSessionRepository(BaseRepository[RefreshSession]):
    model = RefreshSession

    async def create(
        self,
        *,
        jti: uuid.UUID,
        user_id: uuid.UUID,
        family_id: uuid.UUID,
        expires_at: dt.datetime,
    ) -> RefreshSession:
        return await self.add(
            RefreshSession(id=jti, user_id=user_id, family_id=family_id, expires_at=expires_at)
        )

    async def get_for_update(self, jti: uuid.UUID) -> RefreshSession | None:
        """The row, locked until the request's transaction ends.

        The lock is what makes reuse detection work. Without it two refreshes arriving together
        both read `revoked_at IS NULL`, both rotate, and the family ends up with two live chains
        and no way to tell the thief from the owner. With it the second waits, then reads the
        first one's revocation.

        `SELECT ... FOR UPDATE` lives inside the transaction, so it is safe behind pgBouncer's
        transaction pooling; a session-level advisory lock would not be. `populate_existing` makes
        the row read under the lock win over any copy this session already holds.
        """
        stmt = (
            select(RefreshSession)
            .where(RefreshSession.id == jti)
            .with_for_update()
            .execution_options(populate_existing=True)
        )
        row: RefreshSession | None = await self.session.scalar(stmt)
        return row

    async def revoke(self, row: RefreshSession, *, at: dt.datetime) -> None:
        row.revoked_at = at
        await self.session.flush()

    async def revoke_family(self, family_id: uuid.UUID, *, at: dt.datetime) -> None:
        """Revoke every live token descended from one sign-in. Already-revoked rows keep their
        timestamp, which is the record of when each one was actually spent."""
        await self.session.execute(
            update(RefreshSession)
            .where(RefreshSession.family_id == family_id, RefreshSession.revoked_at.is_(None))
            .values(revoked_at=at)
        )
        await self.session.flush()

    async def revoke_all_for_user(self, user_id: uuid.UUID, *, at: dt.datetime) -> None:
        """Every family the user has, on every device."""
        await self.session.execute(
            update(RefreshSession)
            .where(RefreshSession.user_id == user_id, RefreshSession.revoked_at.is_(None))
            .values(revoked_at=at)
        )
        await self.session.flush()
