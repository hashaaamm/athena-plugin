"""THE object graph: repository -> service -> facade.

The project structure rules MUST that construction happens here and nowhere else,
because this is the single boundary a test can override. A view that builds its own service cannot
be overridden, and a graph assembled in three files cannot be reasoned about.

Views import only the `...Dep` aliases at the bottom.
"""

from __future__ import annotations

from typing import Annotated

from fastapi import Depends
{%- if cookiecutter.use_postgres == "yes" %}
from sqlalchemy.ext.asyncio import AsyncSession
{%- endif %}

from app.core.config import Settings, get_settings
{%- if cookiecutter.use_postgres == "yes" %}
from app.core.database import get_db_session
{%- endif %}
from app.facades.health_facade import HealthFacade
{%- if cookiecutter.use_postgres == "yes" %}
from app.facades.item_facade import ItemFacade
from app.repositories.health_repository import HealthRepository
from app.repositories.item_repository import ItemRepository
{%- endif %}
from app.services.health_service import HealthService
{%- if cookiecutter.use_postgres == "yes" %}
from app.services.item_service import ItemService
{%- endif %}

SettingsDep = Annotated[Settings, Depends(get_settings)]
{%- if cookiecutter.use_postgres == "yes" %}
SessionDep = Annotated[AsyncSession, Depends(get_db_session)]


# --- Item ----------------------------------------------------------------


def get_item_repository(session: SessionDep) -> ItemRepository:
    return ItemRepository(session)


def get_item_service(
    repository: Annotated[ItemRepository, Depends(get_item_repository)],
) -> ItemService:
    return ItemService(repository)


def get_item_facade(
    service: Annotated[ItemService, Depends(get_item_service)],
) -> ItemFacade:
    return ItemFacade(service)


ItemFacadeDep = Annotated[ItemFacade, Depends(get_item_facade)]
{%- endif %}


# --- Health --------------------------------------------------------------

{% if cookiecutter.use_postgres == "yes" %}
def get_health_repository(session: SessionDep) -> HealthRepository:
    return HealthRepository(session)


def get_health_service(
    repository: Annotated[HealthRepository, Depends(get_health_repository)],
) -> HealthService:
    return HealthService(repository)
{% else %}
def get_health_service() -> HealthService:
    return HealthService()
{% endif %}

def get_health_facade(
    service: Annotated[HealthService, Depends(get_health_service)],
    settings: SettingsDep,
) -> HealthFacade:
    return HealthFacade(service, version=settings.git_sha)


HealthFacadeDep = Annotated[HealthFacade, Depends(get_health_facade)]
