"""`REFRESH_TRANSPORT=cookie`: the refresh token as an httpOnly cookie, for a browser client.

Login, refresh and change-password hand it to the browser as a cookie and return only the access
token; refresh and logout read it from that cookie and require the CSRF check. The cookie's
attributes and that check are `app/api/refresh_cookie.py`, which is all of the transport.

`CORS_ORIGINS` must list the client's origin, and settings refuse to start without one: the CSRF
check accepts no other. The routes every transport shares, and the public/private split, are in
`auth.py`.
"""

from __future__ import annotations

from fastapi import APIRouter, Depends, Response, status

from app.api.deps import ActorDep, AuthServiceDep, SettingsDep
from app.api.refresh_cookie import (
    RefreshCookie,
    clear_session,
    deliver_session,
    require_browser_caller,
)
from app.schemas.auth import AccessTokenRead, ChangePasswordRequest, LoginRequest
from app.schemas.common import ErrorResponse

public_router = APIRouter(prefix="/auth", tags=["auth"])
private_router = APIRouter(prefix="/auth", tags=["auth"])


@public_router.post(
    "/login",
    response_model=AccessTokenRead,
    responses={401: {"model": ErrorResponse}, 422: {"model": ErrorResponse}},
)
async def login(
    payload: LoginRequest, service: AuthServiceDep, response: Response, settings: SettingsDep
) -> AccessTokenRead:
    """Exchange an email and a password for an access token, and a refresh cookie.

    A wrong password and an unknown address answer identically, in body, status and timing.
    """
    return deliver_session(await service.login(payload), response, settings)


@public_router.post(
    "/refresh",
    response_model=AccessTokenRead,
    dependencies=[Depends(require_browser_caller)],
    responses={401: {"model": ErrorResponse}, 403: {"model": ErrorResponse}},
)
async def refresh(
    service: AuthServiceDep,
    response: Response,
    settings: SettingsDep,
    refresh_token: RefreshCookie = None,
) -> AccessTokenRead:
    """Spend the refresh cookie for a new access token and a new cookie.

    Single-use: the cookie this replaces is revoked, and presenting it again revokes every token
    descended from the same sign-in.
    """
    return deliver_session(await service.refresh(refresh_token), response, settings)


@public_router.post(
    "/logout",
    status_code=status.HTTP_204_NO_CONTENT,
    dependencies=[Depends(require_browser_caller)],
    responses={403: {"model": ErrorResponse}},
)
async def logout(
    service: AuthServiceDep,
    response: Response,
    settings: SettingsDep,
    refresh_token: RefreshCookie = None,
) -> None:
    """End this session and clear its cookie. 204 whether or not there was a session to end."""
    await service.logout(refresh_token)
    clear_session(response, settings)


@private_router.post(
    "/change-password",
    response_model=AccessTokenRead,
    responses={
        401: {"model": ErrorResponse},
        403: {"model": ErrorResponse},
        422: {"model": ErrorResponse},
    },
)
async def change_password(
    payload: ChangePasswordRequest,
    actor: ActorDep,
    service: AuthServiceDep,
    response: Response,
    settings: SettingsDep,
) -> AccessTokenRead:
    """Replace the caller's password. A valid token is not enough; the current password is.

    Ends every session the account has, and starts a new one for the caller: a new access token,
    and a new refresh cookie.
    """
    return deliver_session(await service.change_password(actor.id, payload), response, settings)
