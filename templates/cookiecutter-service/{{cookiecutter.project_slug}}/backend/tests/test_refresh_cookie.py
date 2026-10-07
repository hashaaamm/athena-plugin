"""The refresh cookie: its attributes, its absence from every body, and the CSRF check before it.

Each attribute is asserted from the raw `Set-Cookie` header rather than through the client's jar.
A jar that silently drops a cookie it dislikes would make a missing `HttpOnly` look like a test
setup problem; the header says exactly what the browser was told.
"""

from __future__ import annotations

from collections.abc import AsyncIterator

import pytest
from httpx import AsyncClient, Response

from app.api.refresh_cookie import CSRF_HEADER, REFRESH_COOKIE
from app.core.config import Settings
from tests.helpers import NEW_PASSWORD, ClientFactory, register_payload

ORIGIN = "http://localhost:3000"


@pytest.fixture
async def browser(client_for: ClientFactory) -> AsyncIterator[AsyncClient]:
    """The app as the browser meets it: CORS configured for the SPA's origin."""
    async with client_for(Settings(environment="test", cors_origins=ORIGIN)) as client:
        yield client


def _cookie(response: Response) -> tuple[str, dict[str, str]]:
    """The refresh cookie's value, and its attributes lower-cased."""
    for header in response.headers.get_list("set-cookie"):
        pair, *attributes = (part.strip() for part in header.split(";"))
        name, _, value = pair.partition("=")
        if name == REFRESH_COOKIE:
            parsed = {}
            for attribute in attributes:
                key, _, attribute_value = attribute.partition("=")
                parsed[key.lower()] = attribute_value
            return value, parsed
    raise AssertionError(f"no {REFRESH_COOKIE} cookie was set")


async def _log_in(client: AsyncClient) -> tuple[dict[str, str], Response]:
    payload = register_payload()
    await client.post("/api/v1/auth/register", json=payload)
    response = await client.post(
        "/api/v1/auth/login",
        json={"email": payload["email"], "password": payload["password"]},
    )
    assert response.status_code == 200
    return payload, response


def _browser_headers(token: str, **extra: str) -> dict[str, str]:
    return {"Cookie": f"{REFRESH_COOKIE}={token}", CSRF_HEADER: "fetch", **extra}


async def test_the_refresh_token_is_set_httponly_secure_strict_and_scoped(
    browser: AsyncClient,
) -> None:
    _, response = await _log_in(browser)

    _, attributes = _cookie(response)

    assert "httponly" in attributes
    assert "secure" in attributes
    assert attributes["samesite"] == "strict"
    assert attributes["path"] == "/api/v1/auth"
    assert attributes["max-age"] == str(30 * 24 * 60 * 60)
    # Host-only: no Domain attribute means no other subdomain ever receives it.
    assert "domain" not in attributes


async def test_no_body_ever_carries_the_refresh_token(browser: AsyncClient) -> None:
    """A body that carried it as well would hand it to any script that can call the endpoint —
    exactly the read the cookie exists to remove."""
    payload, login = await _log_in(browser)
    token, _ = _cookie(login)
    refreshed = await browser.post("/api/v1/auth/refresh", headers=_browser_headers(token))
    changed = await browser.post(
        "/api/v1/auth/change-password",
        headers={"Authorization": f"Bearer {refreshed.json()['access_token']}"},
        json={"current_password": payload["password"], "new_password": NEW_PASSWORD},
    )

    for response in (login, refreshed, changed):
        assert response.status_code == 200
        assert "refresh_token" not in response.json()
        assert set(response.json()) == {"access_token", "token_type", "expires_in"}
        assert _cookie(response)[0] not in response.text


async def test_a_refresh_without_the_csrf_header_is_refused_and_spends_nothing(
    browser: AsyncClient,
) -> None:
    """What a cross-site form post looks like: the cookie, and no custom header. Refused before
    the service runs, so the token is still good for the real client."""
    _, login = await _log_in(browser)
    token, _ = _cookie(login)

    forged = await browser.post(
        "/api/v1/auth/refresh", headers={"Cookie": f"{REFRESH_COOKIE}={token}"}
    )
    genuine = await browser.post("/api/v1/auth/refresh", headers=_browser_headers(token))

    assert forged.status_code == 403
    assert genuine.status_code == 200


async def test_a_refresh_from_an_origin_not_on_the_allowlist_is_refused(
    browser: AsyncClient,
) -> None:
    _, login = await _log_in(browser)
    token, _ = _cookie(login)

    foreign = await browser.post(
        "/api/v1/auth/refresh",
        headers=_browser_headers(token, Origin="https://not-us.example"),
    )
    ours = await browser.post(
        "/api/v1/auth/refresh", headers=_browser_headers(token, Origin=ORIGIN)
    )

    assert foreign.status_code == 403
    assert ours.status_code == 200


async def test_a_logout_without_the_csrf_header_ends_nothing(browser: AsyncClient) -> None:
    """Logging somebody out from a third-party page is a nuisance attack, and it is refused."""
    _, login = await _log_in(browser)
    token, _ = _cookie(login)

    forged = await browser.post(
        "/api/v1/auth/logout", headers={"Cookie": f"{REFRESH_COOKIE}={token}"}
    )
    still = await browser.post("/api/v1/auth/refresh", headers=_browser_headers(token))

    assert forged.status_code == 403
    assert still.status_code == 200


async def test_logout_clears_the_cookie(browser: AsyncClient) -> None:
    _, login = await _log_in(browser)
    token, _ = _cookie(login)

    response = await browser.post("/api/v1/auth/logout", headers=_browser_headers(token))

    assert response.status_code == 204
    value, attributes = _cookie(response)
    assert value in ("", '""')
    assert attributes["max-age"] == "0"
    # Same path as when it was set, or the browser keeps the original.
    assert attributes["path"] == "/api/v1/auth"


async def test_logout_with_no_cookie_at_all_is_still_a_204(browser: AsyncClient) -> None:
    response = await browser.post("/api/v1/auth/logout", headers={CSRF_HEADER: "fetch"})

    assert response.status_code == 204


async def test_a_refresh_with_no_cookie_is_a_401(browser: AsyncClient) -> None:
    """The web client's first request after a reload, when nobody is signed in."""
    response = await browser.post("/api/v1/auth/refresh", headers={CSRF_HEADER: "fetch"})

    assert response.status_code == 401
    assert response.headers["www-authenticate"] == "Bearer"


def test_an_insecure_cookie_is_refused_when_deployed() -> None:
    with pytest.raises(ValueError, match="REFRESH_COOKIE_SECURE"):
        Settings(
            environment="production",
            postgres_password="a-real-one",
            refresh_cookie_secure=False,
        )


def test_samesite_none_without_secure_is_refused_everywhere() -> None:
    """Browsers drop that cookie silently. Refusing it here beats a sign-in that never persists."""
    with pytest.raises(ValueError, match="REFRESH_COOKIE_SAMESITE"):
        Settings(environment="local", refresh_cookie_samesite="none", refresh_cookie_secure=False)
