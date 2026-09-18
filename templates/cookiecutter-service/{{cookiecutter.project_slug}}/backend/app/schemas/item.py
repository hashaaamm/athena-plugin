"""Item wire contracts. Typed in and typed out — no `dict[str, Any]` crosses a layer boundary."""

from __future__ import annotations

import datetime as dt
import uuid

from pydantic import BaseModel, ConfigDict, Field


class ItemCreate(BaseModel):
    name: str = Field(min_length=1, max_length=200, examples=["widget"])
    description: str | None = Field(default=None, max_length=2000)


class ItemRead(BaseModel):
    # Required for `model_validate` to accept an ORM instance. Without it the facade fails at
    # runtime rather than at type-check time.
    model_config = ConfigDict(from_attributes=True)

    id: uuid.UUID
    name: str
    description: str | None
    created_at: dt.datetime
    updated_at: dt.datetime


class ItemList(BaseModel):
    items: list[ItemRead]
    count: int
