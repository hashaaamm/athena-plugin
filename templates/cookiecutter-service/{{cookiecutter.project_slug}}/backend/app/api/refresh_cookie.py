"""The refresh token as an httpOnly cookie: set, read, cleared, and defended against CSRF.

This service has a browser client, and a browser has nowhere to keep a credential that script in
the page cannot read — except a cookie marked `HttpOnly`. So the refresh token never appears in a
response body here. Login, refresh and change-password set it as a cookie and return only the
access token, which the web client holds in memory; refresh and logout read it back from the
cookie. Ask Athena about browser token storage for why this shape and not `sessionStorage`.

**The attributes, and what each one is for:**

* `HttpOnly` — invisible to `document.cookie` and to every script, so an XSS payload can use the
  session while the page is open but cannot carry the token off to replay later.
* `Secure` — never sent over plain HTTP. `REFRESH_COOKIE_SECURE`, refused as false when deployed.
* `SameSite` — `strict` by default: not attached to any request a third-party page starts. It needs
  the SPA and the API on one *site*; see `refresh_cookie_samesite` in `app/core/config.py`.
* `Path` — the auth routes only. No other endpoint reads the cookie, so no other request carries it.

**CSRF.** A cookie is attached to requests the browser makes whoever started them, which is the
whole difference between it and a bearer header. Refresh and logout are the only routes that read
it, and both require `require_browser_caller`: a custom request header, which no cross-site form
or plain `fetch` can send without a CORS preflight that only the configured origins pass, and an
`Origin`, when the browser sends one, that is on the same allowlist.
"""

from __future__ import annotations

from typing import Annotated

from fastapi import Cookie, Request, Response

from app.api.deps import SettingsDep
from app.core.config import Settings
from app.core.exceptions import ForbiddenError
from app.schemas.auth import AccessTokenRead, TokenPairRead

#: Namespaced by project. Cookies are scoped by host, not by port, so two services generated from
#: this template and run on `localhost` would otherwise overwrite each other's session.
REFRESH_COOKIE = "{{ cookiecutter.project_slug }}.refresh_token"

#: The header the web client sends on the two cookie-authenticated routes. Its value is not
#: checked — its presence is the defence, because a cross-origin page cannot add one without a
#: preflight the CORS allowlist refuses.
CSRF_HEADER = "X-Requested-With"

#: The refresh token, if the browser sent one. Absent is not an error here: refresh refuses it with
#: the same 401 as a bad token, and logout treats it as nothing to end.
RefreshCookie = Annotated[str | None, Cookie(alias=REFRESH_COOKIE)]


def _path(settings: Settings) -> str:
    return f"{settings.api_v1_prefix}/auth"


def deliver_session(pair: TokenPairRead, response: Response, settings: Settings) -> AccessTokenRead:
    """Put the refresh token in the cookie and return the rest as the body.

    Never both. A body carrying the refresh token as well would hand it to any script that can call
    this endpoint, which is exactly the read the cookie exists to remove.
    """
    response.set_cookie(
        REFRESH_COOKIE,
        pair.refresh_token,
        max_age=settings.refresh_token_ttl_days * 24 * 60 * 60,
        path=_path(settings),
        secure=settings.refresh_cookie_secure,
        httponly=True,
        samesite=settings.refresh_cookie_samesite,
    )
    return AccessTokenRead(access_token=pair.access_token, expires_in=pair.expires_in)


def clear_session(response: Response, settings: Settings) -> None:
    """Tell the browser to drop the cookie. Same name, path and attributes, or it keeps it."""
    response.delete_cookie(
        REFRESH_COOKIE,
        path=_path(settings),
        secure=settings.refresh_cookie_secure,
        httponly=True,
        samesite=settings.refresh_cookie_samesite,
    )


async def require_browser_caller(request: Request, settings: SettingsDep) -> None:
    """Refuse a cookie-authenticated request a third-party page could have started.

    403 rather than 401: the session may be perfectly valid, and the web client must not read this
    as one that has ended.
    """
    if not request.headers.get(CSRF_HEADER):
        raise ForbiddenError(f"The {CSRF_HEADER} header is required")
    origin = request.headers.get("origin")
    if origin is not None and origin not in settings.cors_origin_list:
        raise ForbiddenError("Origin not allowed")
