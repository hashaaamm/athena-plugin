"""One entry point per Item use case, and the one place an ORM object becomes a wire schema.

A facade that is a one-line `model_validate` is fine and is not a wasted layer: it is what keeps
the router stable when a use case grows a second service call, and what stops the service from
learning the wire format. See adr/0004-facade-layer-in-fastapi-services.md.
"""

from __future__ import annotations

import uuid

from app.schemas.item import ItemCreate, ItemList, ItemRead
from app.services.item_service import ItemService


class ItemFacade:
    def __init__(self, items: ItemService) -> None:
        self._items = items

    async def create_item(self, payload: ItemCreate) -> ItemRead:
        item = await self._items.create(name=payload.name, description=payload.description)
        return ItemRead.model_validate(item)

    async def get_item(self, item_id: uuid.UUID) -> ItemRead:
        return ItemRead.model_validate(await self._items.get(item_id))

    async def list_items(self, *, limit: int, offset: int) -> ItemList:
        items = await self._items.list(limit=limit, offset=offset)
        read = [ItemRead.model_validate(item) for item in items]
        return ItemList(items=read, count=len(read))

    async def delete_item(self, item_id: uuid.UUID) -> None:
        await self._items.delete(item_id)
