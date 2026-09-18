"""All Item data access.

A method name containing "and", or containing a business term, belongs in the service instead.
"""

from __future__ import annotations

from sqlalchemy import select

from app.models.item import Item
from app.repositories.base import BaseRepository


class ItemRepository(BaseRepository[Item]):
    model = Item

    async def get_by_name(self, name: str) -> Item | None:
        item: Item | None = await self.session.scalar(select(Item).where(Item.name == name))
        return item

    async def create(self, *, name: str, description: str | None) -> Item:
        return await self.add(Item(name=name, description=description))
