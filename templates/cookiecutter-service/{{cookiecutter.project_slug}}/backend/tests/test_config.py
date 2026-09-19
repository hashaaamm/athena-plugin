"""Settings behaviour that is easy to regress and expensive to discover in production."""

from __future__ import annotations

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
{%- if cookiecutter.include_frontend == "yes" %}


def test_no_browser_origin_is_allowed_by_default() -> None:
    """A service is not reachable from a browser until somebody says which one."""
    assert Settings(environment="local").cors_origin_list == []
{%- endif %}
