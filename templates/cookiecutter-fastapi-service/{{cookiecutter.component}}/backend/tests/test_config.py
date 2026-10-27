"""Settings behaviour that is easy to regress and expensive to discover in production."""

from __future__ import annotations

import pytest

from app.core.config import Settings


def test_local_is_not_deployed() -> None:
    assert Settings(environment="local").is_deployed is False
{%- if cookiecutter.use_postgres == "yes" %}
    # The credential is passed explicitly because a deployed Settings refuses to build without
    # one — that validator is the subject of test_config_database.py, not of this test.
    assert Settings(environment="production", postgres_password="a-real-one").is_deployed is True
{%- else %}
    assert Settings(environment="production").is_deployed is True
{%- endif %}


def test_no_browser_origin_is_allowed_by_default() -> None:
    """A service is not reachable from a browser until somebody says which one."""
    assert Settings(environment="local").cors_origin_list == []


def test_origins_are_parsed_from_one_comma_separated_string() -> None:
    settings = Settings(environment="test", cors_origins=" http://a.test, https://b.test:8443 ,")

    assert settings.cors_origin_list == ["http://a.test", "https://b.test:8443"]


@pytest.mark.parametrize(
    "origin",
    [
        "*",
        "localhost:3000",
        "http://a.test/",
        "http://a.test/app",
        "https://*.example.com",
        "ftp://a.test",
        "http://a.test:port",
    ],
)
def test_an_entry_that_is_not_an_origin_is_refused(origin: str) -> None:
    """Each of these is dead configuration that looks alive, or a wildcard that buys nothing."""
    with pytest.raises(ValueError, match="CORS_ORIGINS"):
        Settings(environment="test", cors_origins=origin)
{%- if cookiecutter.use_postgres == "yes" %}


def test_the_refresh_token_travels_in_the_body_by_default() -> None:
    assert Settings(environment="local").refresh_transport == "body"


def test_an_unknown_refresh_transport_is_refused() -> None:
    with pytest.raises(ValueError, match="refresh_transport"):
        Settings(environment="test", refresh_transport="header")


def test_the_cookie_transport_needs_an_origin() -> None:
    """Without one the CSRF check refuses every refresh, and a sign-in never survives a reload."""
    with pytest.raises(ValueError, match="CORS_ORIGINS"):
        Settings(environment="test", refresh_transport="cookie")

    settings = Settings(
        environment="test", refresh_transport="cookie", cors_origins="http://localhost:3000"
    )
    assert settings.refresh_transport == "cookie"
{%- endif %}
