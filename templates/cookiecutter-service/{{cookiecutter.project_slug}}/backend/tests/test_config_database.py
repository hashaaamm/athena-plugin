"""The database half of the settings contract.

Every case passes the password explicitly. Reading it from the ambient environment would make these
tests pass or fail depending on whose shell they run in, which is the single most common cause of
"green locally, red in CI".
"""

from __future__ import annotations

import pytest

from app.core.config import PLACEHOLDER_SECRET, Settings


def test_placeholder_password_is_rejected_when_deployed() -> None:
    """The point of the validator: a placeholder credential in production *starts*, and a service
    that starts with the wrong credentials fails later and much further from the cause."""
    with pytest.raises(ValueError, match="POSTGRES_PASSWORD"):
        Settings(environment="production", postgres_password=PLACEHOLDER_SECRET)


def test_placeholder_password_is_fine_locally() -> None:
    settings = Settings(environment="local", postgres_password=PLACEHOLDER_SECRET)

    assert settings.database_url.startswith("postgresql+asyncpg://")


def test_override_wins_over_the_parts() -> None:
    """Cloud SQL hands us a Unix socket DSN, which cannot be assembled from host and port."""
    settings = Settings(
        environment="production",
        postgres_password=PLACEHOLDER_SECRET,
        database_url_override="postgresql+asyncpg://u:p@/db?host=/cloudsql/x",
    )

    assert settings.database_url.endswith("/cloudsql/x")
