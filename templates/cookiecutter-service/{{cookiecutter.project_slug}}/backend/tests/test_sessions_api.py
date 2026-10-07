"""Refresh, rotation, reuse detection, logout and revocation, through the full stack.

Every request here runs through the application's own commit-or-rollback decision — see the
`client_for` fixture — which is what makes the reuse tests mean something. A family revoked on the
way to a 401 is a write made by a request that failed; if it were rolled back like any other
failure, the 401 would still go out and the test asserting the *next* token is dead is the only
thing that would notice.
{%- if cookiecutter.include_frontend == "yes" %}

This service has a browser client, so the refresh token travels as an httpOnly cookie. The helpers
below present it the way a browser does, and `test_refresh_cookie.py` covers the cookie itself.
{%- endif %}
"""

from __future__ import annotations

import uuid
from typing import Any

import pytest
from httpx import AsyncClient, Response

{% if cookiecutter.include_frontend == "yes" -%}
from app.api.refresh_cookie import CSRF_HEADER, REFRESH_COOKIE
{% endif -%}
from app.core.config import get_settings
from app.core.security.tokens import TokenType, decode_token
from tests.helpers import NEW_PASSWORD, register_payload

LOGIN = "/api/v1/auth/login"
REFRESH = "/api/v1/auth/refresh"
LOGOUT = "/api/v1/auth/logout"
ME = "/api/v1/auth/me"


def _auth(token: str) -> dict[str, str]:
    return {"Authorization": f"Bearer {token}"}
{% if cookiecutter.include_frontend == "yes" %}

def _refresh_token_in(response: Response) -> str:
    """The refresh token a response set as a cookie. Read from the header, not from the client's
    jar, so a test can present any token it was ever given — including a spent one."""
    for header in response.headers.get_list("set-cookie"):
        name, _, rest = header.partition("=")
        if name == REFRESH_COOKIE:
            return rest.split(";", 1)[0]
    raise AssertionError(f"no {REFRESH_COOKIE} cookie was set")


def _presenting(token: str) -> dict[str, Any]:
    """What a browser sends: the cookie, and the CSRF header the web client adds."""
    return {"headers": {"Cookie": f"{REFRESH_COOKIE}={token}", CSRF_HEADER: "fetch"}}
{% else %}

def _refresh_token_in(response: Response) -> str:
    return str(response.json()["refresh_token"])


def _presenting(token: str) -> dict[str, Any]:
    return {"json": {"refresh_token": token}}
{% endif %}

async def _signed_in(client: AsyncClient) -> tuple[dict[str, Any], str, str]:
    """Register and log in. Returns the payload, an access token and a refresh token."""
    payload = register_payload()
    created = await client.post("/api/v1/auth/register", json=payload)
    assert created.status_code == 201
    login = await client.post(
        LOGIN, json={"email": payload["email"], "password": payload["password"]}
    )
    assert login.status_code == 200
    return payload, str(login.json()["access_token"]), _refresh_token_in(login)


async def _log_in_again(client: AsyncClient, payload: dict[str, Any]) -> str:
    """A second sign-in for the same account — another device. Returns its refresh token."""
    login = await client.post(
        LOGIN, json={"email": payload["email"], "password": payload["password"]}
    )
    assert login.status_code == 200
    return _refresh_token_in(login)


async def _refresh(client: AsyncClient, token: str) -> Response:
    return await client.post(REFRESH, **_presenting(token))


async def _logout(client: AsyncClient, token: str) -> Response:
    return await client.post(LOGOUT, **_presenting(token))


# --- Login ----------------------------------------------------------------


async def test_login_issues_a_refresh_token_that_is_a_refresh_token(client: AsyncClient) -> None:
    _, access, refresh = await _signed_in(client)

    claims = decode_token(refresh, expected=TokenType.REFRESH)

    assert claims.typ == TokenType.REFRESH.value
    assert claims.exp - claims.iat == get_settings().refresh_token_ttl_days * 24 * 60 * 60
    assert refresh != access


# --- Refresh and rotation -------------------------------------------------


async def test_refresh_returns_a_new_pair_that_works(client: AsyncClient) -> None:
    payload, _, first = await _signed_in(client)

    response = await _refresh(client, first)

    assert response.status_code == 200
    second = _refresh_token_in(response)
    assert second != first
    me = await client.get(ME, headers=_auth(response.json()["access_token"]))
    assert me.status_code == 200
    assert me.json()["email"] == payload["email"]
    assert response.json()["expires_in"] == get_settings().access_token_ttl_seconds


async def test_a_refresh_token_is_spent_by_using_it(client: AsyncClient) -> None:
    _, _, first = await _signed_in(client)
    await _refresh(client, first)

    again = await _refresh(client, first)

    assert again.status_code == 401
    assert again.json()["error"]["code"] == "unauthorized"


async def test_a_spent_token_coming_back_revokes_its_whole_family(client: AsyncClient) -> None:
    """The theft case. The legitimate client rotated `first` into `second`; `first` reappearing
    means somebody else holds a copy, and from then on nothing in that family works — including
    the token the legitimate client is holding, which is the point: neither side can be trusted."""
    _, _, first = await _signed_in(client)
    second = _refresh_token_in(await _refresh(client, first))

    replay = await _refresh(client, first)
    after = await _refresh(client, second)

    assert replay.status_code == 401
    assert after.status_code == 401


async def test_reuse_revokes_that_family_and_no_other(client: AsyncClient) -> None:
    """Another device's sign-in is a different family, and a replay on this one says nothing about
    it."""
    payload, _, first = await _signed_in(client)
    other_device = await _log_in_again(client, payload)
    await _refresh(client, first)
    await _refresh(client, first)

    response = await _refresh(client, other_device)

    assert response.status_code == 200


async def test_an_expired_refresh_token_is_refused(
    client: AsyncClient, monkeypatch: pytest.MonkeyPatch
) -> None:
    """Minted through the real login with the lifetime turned negative, so this proves the expiry
    is set and enforced rather than that a test can craft a bad token."""
    monkeypatch.setenv("REFRESH_TOKEN_TTL_DAYS", "-1")
    get_settings.cache_clear()
    _, _, expired = await _signed_in(client)

    response = await _refresh(client, expired)

    assert response.status_code == 401


@pytest.mark.parametrize("token", ["not-a-token", ""], ids=["garbage", "empty"])
async def test_something_that_is_not_a_refresh_token_is_refused(
    client: AsyncClient, token: str
) -> None:
    response = await _refresh(client, token)

    assert response.status_code == 401


async def test_an_access_token_cannot_be_spent_as_a_refresh_token(client: AsyncClient) -> None:
    _, access, _ = await _signed_in(client)

    response = await _refresh(client, access)

    assert response.status_code == 401


# --- The refresh token is not a bearer credential -------------------------


async def test_a_refresh_token_is_refused_as_a_bearer_token(client: AsyncClient) -> None:
    """Same key, same claims, thirty days instead of fifteen minutes. `typ` is the only thing
    stopping it opening every guarded route, and this is the test that fails if it stops."""
    _, _, refresh = await _signed_in(client)

    response = await client.get(ME, headers=_auth(refresh))

    assert response.status_code == 401
    assert response.headers["www-authenticate"] == "Bearer"


# --- Logout ---------------------------------------------------------------


async def test_logout_ends_the_session(client: AsyncClient) -> None:
    _, _, refresh = await _signed_in(client)

    logout = await _logout(client, refresh)
    after = await _refresh(client, refresh)

    assert logout.status_code == 204
    assert after.status_code == 401


async def test_logout_with_a_spent_token_still_ends_the_family(client: AsyncClient) -> None:
    """Any token from the family names it. The live one dies with the one that was presented."""
    _, _, first = await _signed_in(client)
    second = _refresh_token_in(await _refresh(client, first))

    await _logout(client, first)

    assert (await _refresh(client, second)).status_code == 401


async def test_logout_ends_this_session_and_no_other(client: AsyncClient) -> None:
    payload, _, here = await _signed_in(client)
    elsewhere = await _log_in_again(client, payload)

    await _logout(client, here)

    assert (await _refresh(client, elsewhere)).status_code == 200


@pytest.mark.parametrize("token", [None, "not-a-token"], ids=["twice", "garbage"])
async def test_logout_never_fails(client: AsyncClient, token: str | None) -> None:
    """Clients retry logout on a flaky network. An error from it is a support ticket about being
    unable to sign out, and it protects nothing."""
    _, _, refresh = await _signed_in(client)
    await _logout(client, refresh)

    response = await _logout(client, token if token is not None else refresh)

    assert response.status_code == 204


# --- Changing the password ------------------------------------------------


async def test_changing_the_password_ends_every_session_and_starts_a_new_one(
    client: AsyncClient,
) -> None:
    """Otherwise "change your password" does not end the session of whoever knew the old one."""
    payload, access, here = await _signed_in(client)
    elsewhere = await _log_in_again(client, payload)

    changed = await client.post(
        "/api/v1/auth/change-password",
        headers=_auth(access),
        json={"current_password": payload["password"], "new_password": NEW_PASSWORD},
    )

    assert changed.status_code == 200
    assert (await _refresh(client, here)).status_code == 401
    assert (await _refresh(client, elsewhere)).status_code == 401
    fresh = _refresh_token_in(changed)
    assert (await _refresh(client, fresh)).status_code == 200
    me = await client.get(ME, headers=_auth(changed.json()["access_token"]))
    assert me.status_code == 200


async def test_a_refused_password_change_ends_nothing(client: AsyncClient) -> None:
    _, access, refresh = await _signed_in(client)

    refused = await client.post(
        "/api/v1/auth/change-password",
        headers=_auth(access),
        json={"current_password": f"not-{uuid.uuid4().hex}", "new_password": NEW_PASSWORD},
    )

    assert refused.status_code == 403
    assert (await _refresh(client, refresh)).status_code == 200
