{% if cookiecutter.use_postgres == "yes" -%}
"""Aggregates the versioned routers, and decides which of them are open.

Two routers, and the split is the security control. `private` carries `get_current_actor` as a
router-level dependency, so every route mounted on it is authenticated whether or not it says so —
deny by default, enforced where routers are aggregated rather than route by route. Ask Athena for
the authorization rules; they grade that **MUST**.

Adding a resource is one line. Put it on `private` unless you can say out loud why an anonymous
caller may have it, and notice that the function where you make that choice is short.

Built from the settings rather than at import, because `REFRESH_TRANSPORT` decides which half of
the authentication routes exist: `auth_body` (the default — the refresh token in the JSON body)
or `auth_cookie` (an httpOnly cookie, for a browser client). Mounting only one means the OpenAPI
document, and every client generated from it, describes the transport this deployment speaks.

The health probes are not here: they are mounted unprefixed in `app/main.py`, because a load
balancer does not know about `/api/v1`.
"""

from __future__ import annotations

from fastapi import APIRouter, Depends

from app.api.v1 import auth, auth_body, auth_cookie
from app.core.config import Settings
from app.core.security.actor import get_current_actor


def build_api_router(settings: Settings) -> APIRouter:
    """Every versioned route, split into the open ones and the authenticated ones."""
    # No bearer token required. Today: register, login, refresh and logout — the first two are how
    # a caller gets a credential, and the last two take a refresh token, which is never a bearer
    # one.
    public = APIRouter()
    public.include_router(auth.public_router)

    # A verified access token required, for every route on it.
    private = APIRouter(dependencies=[Depends(get_current_actor)])
    private.include_router(auth.private_router)

    if settings.refresh_transport == "cookie":
        public.include_router(auth_cookie.public_router)
        private.include_router(auth_cookie.private_router)
    else:
        public.include_router(auth_body.public_router)
        private.include_router(auth_body.private_router)

    # from app.api.v1 import <resource>
    #
    # private.include_router(<resource>.router)

    api_router = APIRouter()
    api_router.include_router(public)
    api_router.include_router(private)
    return api_router
{%- else -%}
"""Aggregates the versioned routers. One place to see the whole HTTP surface.

Empty on a fresh project: with no database there is nothing to authenticate against and no
resource to serve, so the only endpoints a generated service ships are the health probes — and
those are mounted unprefixed in `app/main.py`, because a load balancer does not know about
`/api/v1`. Every resource router goes here instead, one `include_router` line each.
"""

from __future__ import annotations

from fastapi import APIRouter

api_router = APIRouter()

# from app.api.v1 import <resource>
#
# api_router.include_router(<resource>.router)
{%- endif %}
