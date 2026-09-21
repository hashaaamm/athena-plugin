"""Item business rules, and the Item use cases. The only place either lives.

A public method here is a whole use case: it applies the rules, calls whatever the use case needs,
and returns the wire schema the router sends back. The entity-returning half is the service's
internal surface and a router never reaches it. Ask Athena for the layered architecture rules.

Raises `AppError` subclasses, never `HTTPException`: this service must be callable from a worker or
a CLI with no HTTP stack anywhere.
"""

from __future__ import annotations

import uuid

from app.core.exceptions import ConflictError, NotFoundError
from app.models.item import Item
from app.repositories.item_repository import ItemRepository
from app.schemas.item import ItemCreate, ItemList, ItemRead


class ItemService:
    def __init__(self, items: ItemRepository) -> None:
        self._items = items

    async def create_item(self, payload: ItemCreate) -> ItemRead:
        # Uniqueness is not schema validation: it needs the database, so it belongs here rather
        # than in a Pydantic validator. Ask Athena for the layered architecture rules.
        if await self._items.get_by_name(payload.name) is not None:
            raise ConflictError(f"An item named {payload.name!r} already exists")
        item = await self._items.create(name=payload.name, description=payload.description)
        return ItemRead.model_validate(item)

    async def get_item(self, item_id: uuid.UUID) -> ItemRead:
        return ItemRead.model_validate(await self._entity(item_id))

    async def list_items(self, *, limit: int = 50, offset: int = 0) -> ItemList:
        items = await self._items.list_all(limit=limit, offset=offset)
        read = [ItemRead.model_validate(item) for item in items]
        return ItemList(items=read, count=len(read))

    async def delete_item(self, item_id: uuid.UUID) -> None:
        await self._items.delete(await self._entity(item_id))

    async def _entity(self, item_id: uuid.UUID) -> Item:
        """The row, not the response: the service's internal surface.

        Underscored because only this service calls it today. A second service needing the row gets
        a public method with the same body; a router never gets one either way, because that would
        put an ORM instance in the view layer.
        """
        item = await self._items.get(item_id)
        if item is None:
            raise NotFoundError(f"Item {item_id} not found")
        return item
