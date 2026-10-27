"""Readiness' one query.

It lives here and not in the route because `SELECT 1` is still SQL, and SQL outside the data-access
layer is exactly the exception the layer contracts exist to prevent — including the convenient one.
"""

from __future__ import annotations

from sqlalchemy import text
from sqlalchemy.ext.asyncio import AsyncSession


class HealthRepository:
    def __init__(self, session: AsyncSession) -> None:
        self.session = session

    async def ping(self) -> bool:
        return bool(await self.session.scalar(text("SELECT 1")) == 1)
