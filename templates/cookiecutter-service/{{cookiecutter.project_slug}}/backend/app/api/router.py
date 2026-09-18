"""Aggregates the versioned routers. One place to see the whole HTTP surface."""

from __future__ import annotations

from fastapi import APIRouter
{% if cookiecutter.use_postgres == "yes" %}
from app.api.v1 import item

api_router = APIRouter()
api_router.include_router(item.router)
{%- else %}
api_router = APIRouter()
{%- endif %}
