"""The signing key is the whole of the auth system. These are the tests that keep it out of code.

Every case passes the values explicitly. Reading them from the ambient environment would make
these tests pass or fail depending on whose shell they run in, which is the single most common
cause of "green locally, red in CI".
"""

from __future__ import annotations

import pytest

from app.core.config import MIN_JWT_SECRET_LENGTH, PLACEHOLDER_JWT_SECRET, Settings
from app.core.security.tokens import require_signing_key

A_REAL_SECRET = "s" * MIN_JWT_SECRET_LENGTH


def _deployed(**overrides: object) -> Settings:
    return Settings(environment="production", postgres_password="a-real-one", **overrides)


def test_the_sentinel_key_is_refused_when_deployed() -> None:
    """A service that starts with it signs sessions anybody who has read this template can forge,
    and it fails silently because everything works."""
    with pytest.raises(ValueError, match="JWT_SECRET"):
        require_signing_key(_deployed(jwt_secret=PLACEHOLDER_JWT_SECRET))


def test_a_short_key_is_refused_when_deployed() -> None:
    """HS256 against a short key is brute-forceable offline from a single captured token, and
    captured tokens are the normal case. RFC 7518 puts the floor at the hash's output size, and
    PyJWT warns below it — which this project's `filterwarnings = ["error"]` turns into a failure
    before anybody reads the warning."""
    with pytest.raises(ValueError, match="JWT_SECRET"):
        require_signing_key(_deployed(jwt_secret="s" * (MIN_JWT_SECRET_LENGTH - 1)))


def test_a_real_key_starts() -> None:
    require_signing_key(_deployed(jwt_secret=A_REAL_SECRET))


def test_the_sentinel_is_fine_locally() -> None:
    """Otherwise `just dev` on a fresh clone needs a secret before it can say hello, and the
    migration job — which is granted no key, because it signs nothing — could not boot at all."""
    settings = Settings(environment="local")

    require_signing_key(settings)

    assert settings.jwt_secret.get_secret_value() == PLACEHOLDER_JWT_SECRET


def test_building_the_app_is_what_enforces_it() -> None:
    """The check is in `create_app`, so it runs for every process that can issue a token and for
    no process that cannot. A test that only called `require_signing_key` would pass while
    somebody deleted the call."""
    from app.main import create_app

    with pytest.raises(ValueError, match="JWT_SECRET"):
        create_app(_deployed(jwt_secret=PLACEHOLDER_JWT_SECRET))


def test_the_key_is_not_printable() -> None:
    """`SecretStr` is what stops a settings dump putting the key in a log line."""
    assert PLACEHOLDER_JWT_SECRET not in repr(Settings(environment="local"))
