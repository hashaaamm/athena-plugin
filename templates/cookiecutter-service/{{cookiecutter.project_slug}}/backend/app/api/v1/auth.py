"""Authentication routes. Each one binds input, calls exactly one service method, and returns.

There is nothing here worth unit testing. That is the point. Ask Athena for the layered
architecture rules.

**Two routers, and which one a route is on is the security decision.** `public_router` is mounted
bare; `private_router` is mounted behind `get_current_actor` in `app/api/router.py`, so a route
added to it is authenticated without saying anything and a route added to the wrong one is a
mistake visible in a five-line file. That is the only kind of security review that survives contact
with a growing team.
"""

from __future__ import annotations

from fastapi import APIRouter, status

from app.api.deps import ActorDep, AuthServiceDep
from app.schemas.auth import (
    AccessTokenRead,
    ChangePasswordRequest,
    LoginRequest,
    RegisterRequest,
    UserRead,
)
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


@public_router.post(
    "/login",
    response_model=AccessTokenRead,
    responses={401: {"model": ErrorResponse}, 422: {"model": ErrorResponse}},
)
async def login(payload: LoginRequest, service: AuthServiceDep) -> AccessTokenRead:
    """Exchange an email and a password for an access token.

    A wrong password and an unknown address answer identically, in body, status and timing.
    """
    return await service.login(payload)


@private_router.get(
    "/me",
    response_model=UserRead,
    responses={401: {"model": ErrorResponse}},
)
async def me(actor: ActorDep, service: AuthServiceDep) -> UserRead:
    return await service.me(actor.id)


@private_router.post(
    "/change-password",
    status_code=status.HTTP_204_NO_CONTENT,
    responses={
        401: {"model": ErrorResponse},
        403: {"model": ErrorResponse},
        422: {"model": ErrorResponse},
    },
)
async def change_password(
    payload: ChangePasswordRequest, actor: ActorDep, service: AuthServiceDep
) -> None:
    """Replace the caller's password. A valid token is not enough; the current password is."""
    await service.change_password(actor.id, payload)
