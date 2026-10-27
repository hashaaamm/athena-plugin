"""Generic data access. With `app/models`, the only layers allowed to import SQLAlchemy.

`UserRepository` is what a subclass looks like: `class XRepository(BaseRepository[X])` with
`model = X`, inheriting the four methods below before it writes a query of its own.

Repositories `flush()`; they never `commit()`. `flush()` is what makes generated ids and constraint
violations surface inside the caller's transaction, while it can still be rolled back.
Ask Athena for the layered architecture rules; AGENTS.md rule 5 says the same thing.
"""

from __future__ import annotations

import uuid
from collections.abc import Sequence

from sqlalchemy import select
from sqlalchemy.ext.asyncio import AsyncSession

from app.models.base import Base


class BaseRepository[ModelType: Base]:
    model: type[ModelType]

    def __init__(self, session: AsyncSession) -> None:
        self.session = session

    async def get(self, entity_id: uuid.UUID) -> ModelType | None:
        return await self.session.get(self.model, entity_id)

    async def list_all(self, *, limit: int = 100, offset: int = 0) -> Sequence[ModelType]:
        stmt = select(self.model).limit(limit).offset(offset)
        return (await self.session.scalars(stmt)).all()

    async def add(self, entity: ModelType) -> ModelType:
        self.session.add(entity)
        await self.session.flush()
        return entity

    async def delete(self, entity: ModelType) -> None:
        await self.session.delete(entity)
        await self.session.flush()
