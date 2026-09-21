"""Development seed data. `just db-seed`.

Lives at the top of `app/`, not in a layer: it is an entry point, exactly like a route or a worker
task. It opens its own session, builds a request schema and calls one service method. No domain
logic here, and no shortcut around the service either — a non-HTTP caller uses the same use-case
surface a router does, which is the trade the layering makes.

Seed the awkward cases too, not just a happy row — they are the ones nobody remembers to create
by hand when reproducing a bug.
"""

from __future__ import annotations

import asyncio

import structlog

from app.core.database import dispose_engine, get_session_factory
from app.core.exceptions import ConflictError
from app.repositories.item_repository import ItemRepository
from app.schemas.item import ItemCreate
from app.services.item_service import ItemService

logger = structlog.get_logger(__name__)

SEED_ITEMS = [
    ("first-item", "A perfectly ordinary item."),
    ("item-with-no-description", None),
    ("item-with-a-very-long-name-" + "x" * 150, "Exercises the 200-character column bound."),
]


async def seed() -> None:
    async with get_session_factory()() as session:
        service = ItemService(ItemRepository(session))
        for name, description in SEED_ITEMS:
            try:
                await service.create_item(ItemCreate(name=name, description=description))
            except ConflictError:
                # Seeding is re-runnable on purpose: `just db-seed` twice must not fail.
                logger.info("seed_skipped", name=name)
        await session.commit()
    await dispose_engine()


if __name__ == "__main__":
    asyncio.run(seed())
