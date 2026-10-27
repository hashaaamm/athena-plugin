"""Refresh, rotation, reuse detection, logout and revocation, through the full stack — once per
refresh-token transport. Every test runs under both: the rules are the service's and must not
depend on where the token travels. `test_refresh_cookie.py` covers the cookie itself.

Every request here runs through the application's own commit-or-rollback decision — see the
`client_for` fixture — which is what makes the reuse tests mean something. A family revoked on the
way to a 401 is a write made by a request that failed; if it were rolled back like any other
failure, the 401 would still go out and the test asserting the *next* token is dead is the only
thing that would notice.
"""

from __future__ import annotations

import uuid
from collections.abc import AsyncIterator
from dataclasses import dataclass
from typing import Any

import pytest
from httpx import AsyncClient, Response

from app.api.refresh_cookie import CSRF_HEADER, REFRESH_COOKIE
from app.core.config import RefreshTransport, Settings, get_settings
from app.core.security.tokens import TokenType, decode_token
from tests.helpers import NEW_PASSWORD, ClientFactory, register_payload

LOGIN = "/api/v1/auth/login"
REFRESH = "/api/v1/auth/refresh"
LOGOUT = "/api/v1/auth/logout"
ME = "/api/v1/auth/me"
ORIGIN = "http://localhost:3000"


def _auth(token: str) -> dict[str, str]:
    return {"Authorization": f"Bearer {token}"}


@dataclass
class Api:
    """An HTTP client, and how it presents and reads back a refresh token under one transport."""

    client: AsyncClient
    transport: RefreshTransport

    def token_in(self, response: Response) -> str:
        """The refresh token a response delivered. In cookie mode it is read from the header, not
        from the client's jar, so a test can present any token it was ever given — including a
        spent one."""
        if self.transport == "body":
            return str(response.json()["refresh_token"])
        for header in response.headers.get_list("set-cookie"):
            name, _, rest = header.partition("=")
            if name == REFRESH_COOKIE:
                return rest.split(";", 1)[0]
        raise AssertionError(f"no {REFRESH_COOKIE} cookie was set")

    def presenting(self, token: str) -> dict[str, Any]:
        """What the caller sends. A browser sends the cookie, and the CSRF header the web client
        adds."""
        if self.transport == "body":
            return {"json": {"refresh_token": token}}
        return {"headers": {"Cookie": f"{REFRESH_COOKIE}={token}", CSRF_HEADER: "fetch"}}

    async def signed_in(self) -> tuple[dict[str, Any], str, str]:
        """Register and log in. Returns the payload, an access token and a refresh token."""
        payload = register_payload()
        created = await self.client.post("/api/v1/auth/register", json=payload)
        assert created.status_code == 201
        login = await self.client.post(
            LOGIN, json={"email": payload["email"], "password": payload["password"]}
        )
        assert login.status_code == 200
        return payload, str(login.json()["access_token"]), self.token_in(login)

    async def log_in_again(self, payload: dict[str, Any]) -> str:
        """A second sign-in for the same account — another device. Returns its refresh token."""
        login = await self.client.post(
            LOGIN, json={"email": payload["email"], "password": payload["password"]}
        )
        assert login.status_code == 200
        return self.token_in(login)

    async def refresh(self, token: str) -> Response:
        return await self.client.post(REFRESH, **self.presenting(token))

    async def logout(self, token: str) -> Response:
        return await self.client.post(LOGOUT, **self.presenting(token))


@pytest.fixture(params=["body", "cookie"])
async def api(request: pytest.FixtureRequest, client_for: ClientFactory) -> AsyncIterator[Api]:
    transport: RefreshTransport = request.param
    settings = Settings(environment="test", refresh_transport=transport, cors_origins=ORIGIN)
    async with client_for(settings) as client:
        yield Api(client, transport)


# --- Login ----------------------------------------------------------------


async def test_login_issues_a_refresh_token_that_is_a_refresh_token(api: Api) -> None:
    _, access, refresh = await api.signed_in()

    claims = decode_token(refresh, expected=TokenType.REFRESH)

    assert claims.typ == TokenType.REFRESH.value
    assert claims.exp - claims.iat == get_settings().refresh_token_ttl_days * 24 * 60 * 60
    assert refresh != access


# --- Refresh and rotation -------------------------------------------------


async def test_refresh_returns_a_new_pair_that_works(api: Api) -> None:
    payload, _, first = await api.signed_in()

    response = await api.refresh(first)

    assert response.status_code == 200
    second = api.token_in(response)
    assert second != first
    me = await api.client.get(ME, headers=_auth(response.json()["access_token"]))
    assert me.status_code == 200
    assert me.json()["email"] == payload["email"]
    assert response.json()["expires_in"] == get_settings().access_token_ttl_seconds


async def test_a_refresh_token_is_spent_by_using_it(api: Api) -> None:
    _, _, first = await api.signed_in()
    await api.refresh(first)

    again = await api.refresh(first)

    assert again.status_code == 401
    assert again.json()["error"]["code"] == "unauthorized"


async def test_a_spent_token_coming_back_revokes_its_whole_family(api: Api) -> None:
    """The theft case. The legitimate client rotated `first` into `second`; `first` reappearing
    means somebody else holds a copy, and from then on nothing in that family works — including
    the token the legitimate client is holding, which is the point: neither side can be trusted."""
    _, _, first = await api.signed_in()
    second = api.token_in(await api.refresh(first))

    replay = await api.refresh(first)
    after = await api.refresh(second)

    assert replay.status_code == 401
    assert after.status_code == 401


async def test_reuse_revokes_that_family_and_no_other(api: Api) -> None:
    """Another device's sign-in is a different family, and a replay on this one says nothing about
    it."""
    payload, _, first = await api.signed_in()
    other_device = await api.log_in_again(payload)
    await api.refresh(first)
    await api.refresh(first)

    response = await api.refresh(other_device)

    assert response.status_code == 200


async def test_an_expired_refresh_token_is_refused(
    api: Api, monkeypatch: pytest.MonkeyPatch
) -> None:
    """Minted through the real login with the lifetime turned negative, so this proves the expiry
    is set and enforced rather than that a test can craft a bad token."""
    monkeypatch.setenv("REFRESH_TOKEN_TTL_DAYS", "-1")
    get_settings.cache_clear()
    _, _, expired = await api.signed_in()

    response = await api.refresh(expired)

    assert response.status_code == 401


@pytest.mark.parametrize("token", ["not-a-token", ""], ids=["garbage", "empty"])
async def test_something_that_is_not_a_refresh_token_is_refused(api: Api, token: str) -> None:
    response = await api.refresh(token)

    assert response.status_code == 401


async def test_an_access_token_cannot_be_spent_as_a_refresh_token(api: Api) -> None:
    _, access, _ = await api.signed_in()

    response = await api.refresh(access)

    assert response.status_code == 401


# --- The refresh token is not a bearer credential -------------------------


async def test_a_refresh_token_is_refused_as_a_bearer_token(api: Api) -> None:
    """Same key, same claims, thirty days instead of fifteen minutes. `typ` is the only thing
    stopping it opening every guarded route, and this is the test that fails if it stops."""
    _, _, refresh = await api.signed_in()

    response = await api.client.get(ME, headers=_auth(refresh))

    assert response.status_code == 401
    assert response.headers["www-authenticate"] == "Bearer"


# --- Logout ---------------------------------------------------------------


async def test_logout_ends_the_session(api: Api) -> None:
    _, _, refresh = await api.signed_in()

    logout = await api.logout(refresh)
    after = await api.refresh(refresh)

    assert logout.status_code == 204
    assert after.status_code == 401


async def test_logout_with_a_spent_token_still_ends_the_family(api: Api) -> None:
    """Any token from the family names it. The live one dies with the one that was presented."""
    _, _, first = await api.signed_in()
    second = api.token_in(await api.refresh(first))

    await api.logout(first)

    assert (await api.refresh(second)).status_code == 401


async def test_logout_ends_this_session_and_no_other(api: Api) -> None:
    payload, _, here = await api.signed_in()
    elsewhere = await api.log_in_again(payload)

    await api.logout(here)

    assert (await api.refresh(elsewhere)).status_code == 200


@pytest.mark.parametrize("token", [None, "not-a-token"], ids=["twice", "garbage"])
async def test_logout_never_fails(api: Api, token: str | None) -> None:
    """Clients retry logout on a flaky network. An error from it is a support ticket about being
    unable to sign out, and it protects nothing."""
    _, _, refresh = await api.signed_in()
    await api.logout(refresh)

    response = await api.logout(token if token is not None else refresh)

    assert response.status_code == 204


# --- Changing the password ------------------------------------------------


async def test_changing_the_password_ends_every_session_and_starts_a_new_one(api: Api) -> None:
    """Otherwise "change your password" does not end the session of whoever knew the old one."""
    payload, access, here = await api.signed_in()
    elsewhere = await api.log_in_again(payload)

    changed = await api.client.post(
        "/api/v1/auth/change-password",
        headers=_auth(access),
        json={"current_password": payload["password"], "new_password": NEW_PASSWORD},
    )

    assert changed.status_code == 200
    assert (await api.refresh(here)).status_code == 401
    assert (await api.refresh(elsewhere)).status_code == 401
    fresh = api.token_in(changed)
    assert (await api.refresh(fresh)).status_code == 200
    me = await api.client.get(ME, headers=_auth(changed.json()["access_token"]))
    assert me.status_code == 200


async def test_a_refused_password_change_ends_nothing(api: Api) -> None:
    _, access, refresh = await api.signed_in()

    refused = await api.client.post(
        "/api/v1/auth/change-password",
        headers=_auth(access),
        json={"current_password": f"not-{uuid.uuid4().hex}", "new_password": NEW_PASSWORD},
    )

    assert refused.status_code == 403
    assert (await api.refresh(refresh)).status_code == 200


# --- The document a client is generated from ------------------------------


async def test_the_openapi_document_describes_this_transport_and_no_other(api: Api) -> None:
    """A client generated from the document must expect the transport the deployment speaks: a
    browser client typed against `TokenPairRead` would look for a refresh token that never comes."""
    document = (await api.client.get("/openapi.json")).json()
    schemas = set(document["components"]["schemas"])
    login = document["paths"][LOGIN]["post"]["responses"]["200"]["content"]["application/json"]

    body_only = {"TokenPairRead", "RefreshRequest", "LogoutRequest"}
    if api.transport == "body":
        assert login["schema"]["$ref"].endswith("/TokenPairRead")
        assert body_only <= schemas
        assert "AccessTokenRead" not in schemas
    else:
        assert login["schema"]["$ref"].endswith("/AccessTokenRead")
        assert not body_only & schemas
