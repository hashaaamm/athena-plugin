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


async def test_configured_origin_is_echoed_back() -> None:
    async with _client(ORIGIN) as client:
        response = await client.get("/health/live", headers={"Origin": ORIGIN})

    assert response.headers["access-control-allow-origin"] == ORIGIN


async def test_preflight_is_answered() -> None:
    """The OPTIONS request the browser sends before any non-trivial call. Unanswered, every
    POST from the SPA fails before it is ever sent."""
    async with _client(ORIGIN) as client:
        response = await client.options(
            "/health/live",
            headers={"Origin": ORIGIN, "Access-Control-Request-Method": "POST"},
        )

    assert response.status_code == 200
    assert response.headers["access-control-allow-origin"] == ORIGIN


async def test_an_origin_we_did_not_configure_gets_nothing() -> None:
    async with _client(ORIGIN) as client:
        response = await client.get("/health/live", headers={"Origin": "https://not-us.example"})

    assert "access-control-allow-origin" not in response.headers


async def test_no_configuration_means_no_browser_access_at_all() -> None:
    """The default. A service with no web client should not be reachable from one."""
    async with _client("") as client:
        response = await client.get("/health/live", headers={"Origin": ORIGIN})

    assert "access-control-allow-origin" not in response.headers


def test_origins_are_parsed_from_one_comma_separated_string() -> None:
    settings = Settings(environment="test", cors_origins=" http://a.test, http://b.test ,")

    assert settings.cors_origin_list == ["http://a.test", "http://b.test"]
