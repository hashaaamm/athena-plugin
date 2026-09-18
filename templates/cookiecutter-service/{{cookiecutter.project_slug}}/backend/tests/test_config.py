"""Settings behaviour that is easy to regress and expensive to discover in production."""

from __future__ import annotations

from app.core.config import Settings


def test_local_is_not_deployed() -> None:
    assert Settings(environment="local").is_deployed is False
    assert Settings(environment="production").is_deployed is True
