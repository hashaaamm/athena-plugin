"""The decoder, which is the part of the auth path with the vulnerability classes in it.

Every test below crafts a token by hand and asserts it is refused. That is deliberate: a suite
that only ever decodes tokens this service minted proves the happy path and nothing about the
arguments to `jwt.decode`, which is where the CVEs live.
"""

from __future__ import annotations

import datetime as dt
import uuid
import warnings
from typing import Any

import jwt
import pytest

from app.core.config import get_settings
from app.core.exceptions import UnauthorizedError
from app.core.security.tokens import ALGORITHM, TokenType, decode_token, issue_access_token


def _sign(claims: dict[str, Any], *, algorithm: str = ALGORITHM) -> str:
    """A token signed with this service's key but assembled by the test."""
    return jwt.encode(claims, get_settings().jwt_secret.get_secret_value(), algorithm=algorithm)


def _valid_claims(**overrides: Any) -> dict[str, Any]:
    settings = get_settings()
    now = dt.datetime.now(tz=dt.UTC)
    return {
        "sub": str(uuid.uuid4()),
        "typ": TokenType.ACCESS.value,
        "iss": settings.jwt_issuer,
        "aud": settings.jwt_audience,
        "iat": now,
        "exp": now + dt.timedelta(minutes=15),
        "jti": str(uuid.uuid4()),
        **overrides,
    }


def test_a_minted_token_round_trips_and_says_how_long_it_lives() -> None:
    user_id = uuid.uuid4()

    token, expires_in = issue_access_token(user_id=user_id)
    claims = decode_token(token, expected=TokenType.ACCESS)

    assert claims.sub == user_id
    assert claims.typ == TokenType.ACCESS.value
    assert expires_in == get_settings().access_token_ttl_seconds
    assert claims.exp - claims.iat == get_settings().access_token_ttl_seconds


def test_a_tampered_signature_is_refused() -> None:
    token, _ = issue_access_token(user_id=uuid.uuid4())
    head, _, signature = token.rpartition(".")
    flipped = "A" if signature[0] != "A" else "B"

    with pytest.raises(UnauthorizedError):
        decode_token(f"{head}.{flipped}{signature[1:]}", expected=TokenType.ACCESS)


def test_an_expired_token_is_refused(monkeypatch: pytest.MonkeyPatch) -> None:
    """Minted through the real encoder, so this also proves `exp` is actually being set."""
    monkeypatch.setenv("ACCESS_TOKEN_TTL_SECONDS", "-60")
    get_settings.cache_clear()
    token, _ = issue_access_token(user_id=uuid.uuid4())

    with pytest.raises(UnauthorizedError):
        decode_token(token, expected=TokenType.ACCESS)


@pytest.mark.parametrize(
    ("overrides", "reason"),
    [
        ({"exp": None}, "no expiry means a token that verifies for ever"),
        ({"iat": None}, "a required claim is missing"),
        ({"sub": None}, "a token with no subject names nobody"),
        ({"jti": None}, "a token that cannot be named cannot be denied later"),
        ({"typ": None}, "an unlabelled token is usable as any kind"),
        ({"iss": "somebody-else"}, "another service's issuer"),
        ({"aud": "somebody-else"}, "a token meant for a different audience"),
        ({"typ": "refresh"}, "a refresh token presented as a bearer credential"),
    ],
    ids=[
        "no-exp",
        "no-iat",
        "no-sub",
        "no-jti",
        "no-typ",
        "wrong-issuer",
        "wrong-audience",
        "wrong-type",
    ],
)
def test_a_claim_set_this_service_did_not_issue_is_refused(
    overrides: dict[str, Any], reason: str
) -> None:
    claims = _valid_claims()
    for key, value in overrides.items():
        if value is None:
            del claims[key]
        else:
            claims[key] = value

    with pytest.raises(UnauthorizedError):
        decode_token(_sign(claims), expected=TokenType.ACCESS)


def test_a_token_signed_with_a_different_algorithm_is_refused() -> None:
    """`algorithms` is an allowlist, never the algorithm the token names.

    Signed with the service's own key, so the rejection can only be about the algorithm. PyJWT
    wants 64 bytes for HS512 and the local sentinel is 33, so it warns while the test crafts the
    token — suppressed here, and nowhere else, because `filterwarnings = ["error"]` is what makes
    that warning useful everywhere it is not deliberate.
    """
    with warnings.catch_warnings():
        warnings.simplefilter("ignore", jwt.warnings.InsecureKeyLengthWarning)
        token = _sign(_valid_claims(), algorithm="HS512")

    with pytest.raises(UnauthorizedError):
        decode_token(token, expected=TokenType.ACCESS)


def test_an_unsigned_token_is_refused() -> None:
    """`alg=none` is the oldest JWT attack there is, and it still gets shipped."""
    unsigned = jwt.encode(_valid_claims(), key="", algorithm="none")

    with pytest.raises(UnauthorizedError):
        decode_token(unsigned, expected=TokenType.ACCESS)


def test_a_signed_token_with_the_wrong_claim_shapes_is_refused() -> None:
    """It verified, so it came from something holding the key. It is still not one of ours."""
    with pytest.raises(UnauthorizedError):
        decode_token(_sign(_valid_claims(sub="not-a-uuid")), expected=TokenType.ACCESS)
