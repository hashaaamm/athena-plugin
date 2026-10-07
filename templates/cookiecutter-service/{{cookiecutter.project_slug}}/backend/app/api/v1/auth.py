"""Authentication routes. Each one binds input, calls exactly one service method, and returns.

There is nothing here worth unit testing. That is the point. Ask Athena for the layered
architecture rules.

**Two routers, and which one a route is on is the security decision.** `public_router` is mounted
bare; `private_router` is mounted behind `get_current_actor` in `app/api/router.py`, so a route
added to it is authenticated without saying anything and a route added to the wrong one is a
mistake visible in a five-line file. That is the only kind of security review that survives contact
with a growing team.
{%- if cookiecutter.include_frontend == "yes" %}

**The refresh token is a cookie, not a field.** Login, refresh and change-password hand it to the
browser as an httpOnly cookie and return only the access token; refresh and logout read it from
that cookie and require the CSRF header. `app/api/refresh_cookie.py` is all of that — the transport
— and `AuthService` never learns which one is in use.
{%- else %}

**The refresh token travels in the JSON body**, both ways: login, refresh and change-password
return it, and refresh and logout take it. This service has no browser client, so there is no
cookie anywhere. A web client added later must not keep this token in script-readable storage —
ask Athena about browser token storage before giving it one.
{%- endif %}
"""

from __future__ import annotations

{% if cookiecutter.include_frontend == "yes" -%}
from fastapi import APIRouter, Depends, Response, status

from app.api.deps import ActorDep, AuthServiceDep, SettingsDep
from app.api.refresh_cookie import (
    RefreshCookie,
    clear_session,
    deliver_session,
    require_browser_caller,
)
from app.schemas.auth import (
    AccessTokenRead,
    ChangePasswordRequest,
    LoginRequest,
    RegisterRequest,
    UserRead,
)
{%- else -%}
from fastapi import APIRouter, status

from app.api.deps import ActorDep, AuthServiceDep
from app.schemas.auth import (
    ChangePasswordRequest,
    LoginRequest,
    LogoutRequest,
    RefreshRequest,
    RegisterRequest,
    TokenPairRead,
    UserRead,
)
{%- endif %}
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
{% if cookiecutter.include_frontend == "yes" %}

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
{% else %}

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
{% endif %}

@private_router.get(
    "/me",
    response_model=UserRead,
    responses={401: {"model": ErrorResponse}},
)
async def me(actor: ActorDep, service: AuthServiceDep) -> UserRead:
    return await service.me(actor.id)
{% if cookiecutter.include_frontend == "yes" %}

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
{%- else %}

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
{%- endif %}
