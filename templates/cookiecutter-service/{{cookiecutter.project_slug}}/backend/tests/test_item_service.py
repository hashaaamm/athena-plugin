"""Service rules, tested where they live.

These go through a real repository and a real Postgres: the uniqueness rule is only interesting
because the database also enforces it, and a mocked repository would assert nothing about that.
"""

from __future__ import annotations

import uuid

import pytest
from sqlalchemy.ext.asyncio import AsyncSession

from app.core.exceptions import ConflictError, NotFoundError
from app.repositories.item_repository import ItemRepository
from app.services.item_service import ItemService


@pytest.fixture
def service(session: AsyncSession) -> ItemService:
    return ItemService(ItemRepository(session))


async def test_create_returns_a_persisted_entity(service: ItemService) -> None:
    item = await service.create(name="widget", description="a widget")

    assert item.id is not None  # set by flush(), inside the transaction
    assert item.name == "widget"


async def test_duplicate_name_raises_conflict(service: ItemService) -> None:
    await service.create(name="widget", description=None)

    with pytest.raises(ConflictError):
        await service.create(name="widget", description="a second one")


async def test_get_missing_raises_not_found(service: ItemService) -> None:
    with pytest.raises(NotFoundError):
        await service.get(uuid.uuid4())


async def test_delete_removes_the_item(service: ItemService) -> None:
    item = await service.create(name="widget", description=None)

    await service.delete(item.id)

    with pytest.raises(NotFoundError):
        await service.get(item.id)
