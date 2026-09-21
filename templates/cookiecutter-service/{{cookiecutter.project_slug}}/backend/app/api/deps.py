"""THE object graph: repository -> service.

The project structure rules MUST that construction happens here and nowhere else,
because this is the single boundary a test can override. A view that builds its own service cannot
be overridden, and a graph assembled in three files cannot be reasoned about.

Views import only the `...Dep` aliases at the bottom. A new resource adds a block here — a
`get_<x>_repository`, a `get_<x>_service` and one `Annotated` alias — and nothing anywhere else
constructs either of them.
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
from app.core.security.actor import Actor, get_current_actor
from app.repositories.health_repository import HealthRepository
from app.repositories.user_repository import UserRepository
{%- endif %}
{%- if cookiecutter.use_postgres == "yes" %}
from app.services.auth_service import AuthService
{%- endif %}
from app.services.health_service import HealthService

SettingsDep = Annotated[Settings, Depends(get_settings)]
{%- if cookiecutter.use_postgres == "yes" %}
SessionDep = Annotated[AsyncSession, Depends(get_db_session)]
{%- endif %}


# --- Health --------------------------------------------------------------

{% if cookiecutter.use_postgres == "yes" %}
def get_health_repository(session: SessionDep) -> HealthRepository:
    return HealthRepository(session)


def get_health_service(
    repository: Annotated[HealthRepository, Depends(get_health_repository)],
    settings: SettingsDep,
) -> HealthService:
    return HealthService(repository, version=settings.git_sha)
{% else %}
def get_health_service(settings: SettingsDep) -> HealthService:
    return HealthService(version=settings.git_sha)
{% endif %}

HealthServiceDep = Annotated[HealthService, Depends(get_health_service)]
{%- if cookiecutter.use_postgres == "yes" %}


# --- Authentication ------------------------------------------------------


def get_user_repository(session: SessionDep) -> UserRepository:
    return UserRepository(session)


def get_auth_service(
    repository: Annotated[UserRepository, Depends(get_user_repository)],
) -> AuthService:
    return AuthService(repository)


AuthServiceDep = Annotated[AuthService, Depends(get_auth_service)]

#: The verified caller. `app/api/router.py` already applies `get_current_actor` to every private
#: route, so this alias is how a route that needs the caller's id *reads* it — FastAPI resolves
#: the dependency once per request and both uses get the same `Actor`.
ActorDep = Annotated[Actor, Depends(get_current_actor)]
{%- endif %}
