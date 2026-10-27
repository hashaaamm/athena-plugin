"""The row lock that reuse detection stands on.

Without `FOR UPDATE`, two refreshes arriving together both read `revoked_at IS NULL`, both rotate,
and the family ends up with two live chains — and no reliable reuse detection at all. Nothing in a
single-connection test can see that, so this one opens two connections of its own, commits the
rows it needs, and deletes them again.
"""

from __future__ import annotations

import datetime as dt
import uuid

import pytest
from sqlalchemy import delete, text
from sqlalchemy.exc import DBAPIError
from sqlalchemy.ext.asyncio import async_sessionmaker, create_async_engine
from sqlalchemy.pool import NullPool

from app.core.config import Settings
from app.models.refresh_session import RefreshSession
from app.models.user import User
from app.repositories.refresh_session_repository import RefreshSessionRepository


async def test_a_row_read_for_update_is_held_until_its_transaction_ends(
    settings: Settings, _schema_ready: str
) -> None:
    engine = create_async_engine(
        settings.database_url,
        poolclass=NullPool,
        connect_args={"server_settings": {"search_path": _schema_ready}},
    )
    factory = async_sessionmaker(engine, expire_on_commit=False)
    user_id = uuid.uuid4()
    jti = uuid.uuid4()
    try:
        async with factory() as setup:
            email = f"lock-{jti.hex[:8]}@example.com"
            setup.add(User(id=user_id, email=email, hashed_password="-"))
            await setup.flush()
            setup.add(
                RefreshSession(
                    id=jti,
                    user_id=user_id,
                    family_id=uuid.uuid4(),
                    expires_at=dt.datetime.now(tz=dt.UTC) + dt.timedelta(hours=1),
                )
            )
            await setup.commit()

        async with factory() as holder, factory() as contender:
            assert await RefreshSessionRepository(holder).get_for_update(jti) is not None
            # Bounded, so the test fails in a tenth of a second instead of hanging for ever.
            await contender.execute(text("SET LOCAL lock_timeout = '100ms'"))
            with pytest.raises(DBAPIError, match="lock timeout"):
                await RefreshSessionRepository(contender).get_for_update(jti)
            await contender.rollback()
            await holder.rollback()

        async with factory() as after:
            # Released with the holder's transaction: the next reader gets the row at once.
            await after.execute(text("SET LOCAL lock_timeout = '100ms'"))
            assert await RefreshSessionRepository(after).get_for_update(jti) is not None
            await after.rollback()
    finally:
        async with factory() as cleanup:
            # The sessions go with the user: ON DELETE CASCADE.
            await cleanup.execute(delete(User).where(User.id == user_id))
            await cleanup.commit()
        await engine.dispose()
