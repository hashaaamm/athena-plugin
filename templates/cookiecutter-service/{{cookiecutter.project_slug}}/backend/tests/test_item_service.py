"""Service rules and service output, tested where they live.

These go through a real repository and a real Postgres: the uniqueness rule is only interesting
because the database also enforces it, and a mocked repository would assert nothing about that.

The service is also the entry point, so what it hands back is part of its contract — a use-case
method returns a wire schema, never an ORM row. `test_use_case_methods_return_wire_schemas` is the
test that fails when somebody returns the entity because it was right there.
"""

from __future__ import annotations

import uuid

import pytest
from sqlalchemy.ext.asyncio import AsyncSession

from app.core.exceptions import ConflictError, NotFoundError
from app.models.item import Item
from app.repositories.item_repository import ItemRepository
from app.schemas.item import ItemCreate, ItemList, ItemRead
from app.services.item_service import ItemService


@pytest.fixture
def service(session: AsyncSession) -> ItemService:
    return ItemService(ItemRepository(session))


async def test_create_returns_a_persisted_item(service: ItemService) -> None:
    item = await service.create_item(ItemCreate(name="widget", description="a widget"))

    assert item.id is not None  # set by flush(), inside the transaction
    assert item.name == "widget"


async def test_use_case_methods_return_wire_schemas(service: ItemService) -> None:
    created = await service.create_item(ItemCreate(name="widget", description=None))

    assert isinstance(created, ItemRead)
    assert isinstance(await service.get_item(created.id), ItemRead)
    assert isinstance(await service.list_items(), ItemList)
    assert not isinstance(created, Item)


async def test_duplicate_name_raises_conflict(service: ItemService) -> None:
    await service.create_item(ItemCreate(name="widget", description=None))

    with pytest.raises(ConflictError):
        await service.create_item(ItemCreate(name="widget", description="a second one"))


async def test_get_missing_raises_not_found(service: ItemService) -> None:
    with pytest.raises(NotFoundError):
        await service.get_item(uuid.uuid4())


async def test_delete_removes_the_item(service: ItemService) -> None:
    item = await service.create_item(ItemCreate(name="widget", description=None))

    await service.delete_item(item.id)

    with pytest.raises(NotFoundError):
        await service.get_item(item.id)
