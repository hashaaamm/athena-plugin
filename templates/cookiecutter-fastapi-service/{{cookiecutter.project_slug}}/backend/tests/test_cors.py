"""CORS: the difference between a working browser client and a blank page plus a console error.

Worth its own file because the failure is invisible from the server's side — every assertion you
would naturally write against the API still passes. curl gets a 200; the browser gets the same
200 and refuses to hand the body to the page.
"""

from __future__ import annotations

from httpx import ASGITransport, AsyncClient

from app.core.config import Settings
from app.main import create_app

ORIGIN = "http://localhost:3000"


def _client(cors_origins: str) -> AsyncClient:
    app = create_app(Settings(environment="test", cors_origins=cors_origins))
    return AsyncClient(transport=ASGITransport(app=app), base_url="http://test")


async def test_a_configured_origin_is_echoed_back_with_credentials() -> None:
    async with _client(ORIGIN) as client:
        response = await client.get("/health/live", headers={"Origin": ORIGIN})

    assert response.headers["access-control-allow-origin"] == ORIGIN
    assert response.headers["access-control-allow-credentials"] == "true"


async def test_a_preflight_is_answered_and_allows_the_headers_the_client_sends() -> None:
    """The OPTIONS request the browser sends before any non-trivial call. Unanswered, every POST
    from a browser client fails before it is ever sent — and the headers it sends must be allowed
    by name."""
    async with _client(ORIGIN) as client:
        response = await client.options(
            "/api/v1/auth/refresh",
            headers={
                "Origin": ORIGIN,
                "Access-Control-Request-Method": "POST",
                "Access-Control-Request-Headers": "authorization,content-type,x-requested-with",
            },
        )

    assert response.status_code == 200
    assert response.headers["access-control-allow-origin"] == ORIGIN
    allowed = response.headers["access-control-allow-headers"].lower()
    for header in ("authorization", "content-type", "x-requested-with"):
        assert header in allowed


async def test_an_origin_we_did_not_configure_gets_nothing() -> None:
    """No CORS headers — and still its normal answer, so a server-to-server caller that happens to
    send `Origin` is not broken by a browser policy."""
    async with _client(ORIGIN) as client:
        response = await client.get("/health/live", headers={"Origin": "https://not-us.example"})

    assert response.status_code == 200
    assert "access-control-allow-origin" not in response.headers


async def test_no_configuration_means_no_browser_access_at_all() -> None:
    """The default. A service with no web client should not be reachable from one."""
    async with _client("") as client:
        response = await client.get("/health/live", headers={"Origin": ORIGIN})

    assert "access-control-allow-origin" not in response.headers


async def test_errors_carry_the_cors_headers() -> None:
    """Without them the browser reports a CORS failure instead of the 404 (or the 401), and the
    network tab is the only place that tells the truth."""
    async with _client(ORIGIN) as client:
        response = await client.get("/api/v1/nothing-here", headers={"Origin": ORIGIN})

    assert response.status_code == 404
    assert response.headers["access-control-allow-origin"] == ORIGIN
