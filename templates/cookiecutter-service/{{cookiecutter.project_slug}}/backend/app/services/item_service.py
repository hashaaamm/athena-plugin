"""Item business rules. The only place they live.

Raises `AppError` subclasses, never `HTTPException`: this service must be callable from a worker or
a CLI with no HTTP stack anywhere. See docs/rules/backend/layered-architecture.md.
"""

from __future__ import annotations

import uuid
from collections.abc import Sequence

from app.core.exceptions import ConflictError, NotFoundError
from app.models.item import Item
from app.repositories.item_repository import ItemRepository


class ItemService:
    def __init__(self, items: ItemRepository) -> None:
        self._items = items

    async def create(self, *, name: str, description: str | None) -> Item:
        # Uniqueness is not schema validation: it needs the database, so it belongs here rather
        # than in a Pydantic validator. See docs/rules/backend/layered-architecture.md.
        if await self._items.get_by_name(name) is not None:
            raise ConflictError(f"An item named {name!r} already exists")
        return await self._items.create(name=name, description=description)

    async def get(self, item_id: uuid.UUID) -> Item:
        item = await self._items.get(item_id)
        if item is None:
            raise NotFoundError(f"Item {item_id} not found")
        return item

    async def list(self, *, limit: int = 50, offset: int = 0) -> Sequence[Item]:
        return await self._items.list_all(limit=limit, offset=offset)

    async def delete(self, item_id: uuid.UUID) -> None:
        await self._items.delete(await self.get(item_id))
