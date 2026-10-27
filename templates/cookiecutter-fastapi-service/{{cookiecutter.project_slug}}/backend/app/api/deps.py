"""THE object graph: repository -> service.

The project structure rules MUST that construction happens here and nowhere else,
because this is the single boundary a test can override. A view that builds its own service cannot
be overridden, and a graph assembled in three files cannot be reasoned about.

Views import only the `...Dep` aliases at the bottom. A new resource adds a block here — a
`get_<x>_repository`, a `get_<x>_service` and one `Annotated` alias — and nothing anywhere else
constructs either of them.

Every provider is `async def`, including the ones that await nothing. FastAPI runs a plain `def`
dependency on the AnyIO threadpool — forty threads shared with every other sync call — so a sync
provider that only builds an object costs a thread hop per request and queues behind whatever has
saturated the pool.
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
from app.repositories.refresh_session_repository import RefreshSessionRepository
from app.repositories.user_repository import UserRepository
{%- endif %}
{%- if cookiecutter.use_postgres == "yes" %}
from app.services.auth_service import AuthService
{%- endif %}
from app.services.health_service import HealthService


async def get_request_settings() -> Settings:
    """`get_settings()` for the request graph, and the seam a test overrides.

    `get_settings` stays a plain function because config loading, Alembic and the CLI call it
    outside any request. Depending on it directly would put a cached lookup on the threadpool.
    """
    return get_settings()


SettingsDep = Annotated[Settings, Depends(get_request_settings)]
{%- if cookiecutter.use_postgres == "yes" %}
#: `scope="function"` is what makes the commit happen before the response is sent. With the
#: default scope FastAPI runs the code after `yield` — `get_db_session`'s commit — once the
#: response has already gone, so a commit that fails leaves the client holding a 2xx for a write
#: that never happened. With it, a failed commit is a 500 the client sees.
SessionDep = Annotated[AsyncSession, Depends(get_db_session, scope="function")]
{%- endif %}


# --- Health --------------------------------------------------------------

{% if cookiecutter.use_postgres == "yes" %}
async def get_health_repository(session: SessionDep) -> HealthRepository:
    return HealthRepository(session)


async def get_health_service(
    repository: Annotated[HealthRepository, Depends(get_health_repository)],
    settings: SettingsDep,
) -> HealthService:
    return HealthService(repository, version=settings.git_sha)
{% else %}
async def get_health_service(settings: SettingsDep) -> HealthService:
    return HealthService(version=settings.git_sha)
{% endif %}

HealthServiceDep = Annotated[HealthService, Depends(get_health_service)]
{%- if cookiecutter.use_postgres == "yes" %}


# --- Authentication ------------------------------------------------------


async def get_user_repository(session: SessionDep) -> UserRepository:
    return UserRepository(session)


async def get_refresh_session_repository(session: SessionDep) -> RefreshSessionRepository:
    return RefreshSessionRepository(session)


async def get_auth_service(
    users: Annotated[UserRepository, Depends(get_user_repository)],
    sessions: Annotated[RefreshSessionRepository, Depends(get_refresh_session_repository)],
) -> AuthService:
    return AuthService(users, sessions)


AuthServiceDep = Annotated[AuthService, Depends(get_auth_service)]

#: The verified caller. `app/api/router.py` already applies `get_current_actor` to every private
#: route, so this alias is how a route that needs the caller's id *reads* it — FastAPI resolves
#: the dependency once per request and both uses get the same `Actor`.
ActorDep = Annotated[Actor, Depends(get_current_actor)]
{%- endif %}
