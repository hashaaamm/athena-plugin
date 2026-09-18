"""Item routes. Each one binds input, calls exactly one facade method, and returns.

There is nothing here worth unit testing. That is the point. Ask Athena for the layered
architecture rules.
"""

from __future__ import annotations

import uuid

from fastapi import APIRouter, Query, status

from app.api.deps import ItemFacadeDep
from app.schemas.common import ErrorResponse
from app.schemas.item import ItemCreate, ItemList, ItemRead

router = APIRouter(prefix="/items", tags=["items"])


@router.post(
    "",
    response_model=ItemRead,
    status_code=status.HTTP_201_CREATED,
    # Documented so a generated client can handle them. An undocumented status code is one the
    # client discovers in production.
    responses={409: {"model": ErrorResponse}, 422: {"model": ErrorResponse}},
)
async def create_item(payload: ItemCreate, facade: ItemFacadeDep) -> ItemRead:
    return await facade.create_item(payload)


@router.get("", response_model=ItemList)
async def list_items(
    facade: ItemFacadeDep,
    limit: int = Query(default=50, ge=1, le=200),
    offset: int = Query(default=0, ge=0),
) -> ItemList:
    return await facade.list_items(limit=limit, offset=offset)


@router.get("/{item_id}", response_model=ItemRead, responses={404: {"model": ErrorResponse}})
async def get_item(item_id: uuid.UUID, facade: ItemFacadeDep) -> ItemRead:
    return await facade.get_item(item_id)


@router.delete(
    "/{item_id}",
    status_code=status.HTTP_204_NO_CONTENT,
    responses={404: {"model": ErrorResponse}},
)
async def delete_item(item_id: uuid.UUID, facade: ItemFacadeDep) -> None:
    await facade.delete_item(item_id)
