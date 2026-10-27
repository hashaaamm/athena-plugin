"""`REFRESH_TRANSPORT=body`: the refresh token in the JSON body, both ways.

Login, refresh and change-password return it, and refresh and logout take it. Right for a
server-to-server or mobile caller, which has somewhere safe to keep a credential. A browser does
not: script in the page can read anything a response body hands it, which is why a browser client
runs against `REFRESH_TRANSPORT=cookie` instead — see `auth_cookie.py`, and ask Athena about
browser token storage.

The routes every transport shares, and the public/private split, are in `auth.py`.
"""

from __future__ import annotations

from fastapi import APIRouter, status

from app.api.deps import ActorDep, AuthServiceDep
from app.schemas.auth import (
    ChangePasswordRequest,
    LoginRequest,
    LogoutRequest,
    RefreshRequest,
    TokenPairRead,
)
from app.schemas.common import ErrorResponse

public_router = APIRouter(prefix="/auth", tags=["auth"])
private_router = APIRouter(prefix="/auth", tags=["auth"])


@public_router.post(
    "/login",
    response_model=TokenPairRead,
    responses={401: {"model": ErrorResponse}, 422: {"model": ErrorResponse}},
)
async def login(payload: LoginRequest, service: AuthServiceDep) -> TokenPairRead:
    """Exchange an email and a password for an access token and a refresh token.

    A wrong password and an unknown address answer identically, in body, status and timing.
    """
    return await service.login(payload)


@public_router.post(
    "/refresh",
    response_model=TokenPairRead,
    responses={401: {"model": ErrorResponse}, 422: {"model": ErrorResponse}},
)
async def refresh(payload: RefreshRequest, service: AuthServiceDep) -> TokenPairRead:
    """Spend a refresh token for a new pair.

    Single-use: the token presented is revoked, and presenting it again revokes every token
    descended from the same sign-in. Store the new refresh token before using the new access token.
    """
    return await service.refresh(payload.refresh_token.get_secret_value())


@public_router.post(
    "/logout",
    status_code=status.HTTP_204_NO_CONTENT,
    responses={422: {"model": ErrorResponse}},
)
async def logout(payload: LogoutRequest, service: AuthServiceDep) -> None:
    """End the session a refresh token belongs to. 204 whether or not there was one to end."""
    await service.logout(payload.refresh_token.get_secret_value())


@private_router.post(
    "/change-password",
    response_model=TokenPairRead,
    responses={
        401: {"model": ErrorResponse},
        403: {"model": ErrorResponse},
        422: {"model": ErrorResponse},
    },
)
async def change_password(
    payload: ChangePasswordRequest, actor: ActorDep, service: AuthServiceDep
) -> TokenPairRead:
    """Replace the caller's password. A valid token is not enough; the current password is.

    Ends every session the account has, and starts a new one for the caller: the pair returned
    here replaces the one the caller was holding.
    """
    return await service.change_password(actor.id, payload)
