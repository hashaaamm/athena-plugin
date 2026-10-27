"""Authentication routes. Each one binds input, calls exactly one service method, and returns.

There is nothing here worth unit testing. That is the point. Ask Athena for the layered
architecture rules.

**Two routers, and which one a route is on is the security decision.** `public_router` is mounted
bare; `private_router` is mounted behind `get_current_actor` in `app/api/router.py`, so a route
added to it is authenticated without saying anything and a route added to the wrong one is a
mistake visible in one short function. That is the only kind of security review that survives
contact with a growing team.

**Where the refresh token travels is the only thing the two transports disagree on**, so this file
holds the routes they share — register and me — and the four that differ live beside it, one file
per `REFRESH_TRANSPORT`:

* `auth_body.py` — `body`, the default. Login, refresh and change-password return the refresh token
  in the JSON body, and refresh and logout take it back the same way. Right for server-to-server and
  mobile callers.
* `auth_cookie.py` — `cookie`, for a browser client. The refresh token is an httpOnly cookie that no
  script in the page can read; `app/api/refresh_cookie.py` is all of that transport.

`app/api/router.py` mounts one or the other, and `AuthService` never learns which.
"""

from __future__ import annotations

from fastapi import APIRouter, status

from app.api.deps import ActorDep, AuthServiceDep
from app.schemas.auth import RegisterRequest, UserRead
from app.schemas.common import ErrorResponse

public_router = APIRouter(prefix="/auth", tags=["auth"])
private_router = APIRouter(prefix="/auth", tags=["auth"])


@public_router.post(
    "/register",
    response_model=UserRead,
    status_code=status.HTTP_201_CREATED,
    # Documented so a generated client can handle them. An undocumented status code is one the
    # client discovers in production.
    responses={409: {"model": ErrorResponse}, 422: {"model": ErrorResponse}},
)
async def register(payload: RegisterRequest, service: AuthServiceDep) -> UserRead:
    """Create an account. Returns the user and no session — log in next."""
    return await service.register(payload)


@private_router.get(
    "/me",
    response_model=UserRead,
    responses={401: {"model": ErrorResponse}},
)
async def me(actor: ActorDep, service: AuthServiceDep) -> UserRead:
    return await service.me(actor.id)
