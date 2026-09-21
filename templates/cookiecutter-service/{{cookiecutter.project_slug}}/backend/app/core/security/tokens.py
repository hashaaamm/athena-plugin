"""Minting and verifying access tokens. PyJWT, HS256, one encoder and one decoder.

PyJWT signs and verifies JWTs and does nothing else, which is why it is the approved default: no
JWE, no key sets, no JOSE surface nothing here uses. Ask Athena which auth libraries are approved
before reaching for `python-jose` — the tutorials' default answer is banned, for algorithm-confusion
advisories and for accepting `alg=none`.

HS256 is right while one service both signs and verifies. The moment a second service needs to
verify a token this one issued, HS256 means handing it the *signing* key; that is when this becomes
RS256 or EdDSA with a published JWKS, and it is a bigger change than editing this file.
"""

from __future__ import annotations

import datetime as dt
import uuid
from enum import StrEnum
from typing import Any

import jwt
from pydantic import BaseModel, ConfigDict

from app.core.config import (
    MIN_JWT_SECRET_LENGTH,
    PLACEHOLDER_JWT_SECRET,
    Settings,
    get_settings,
)
from app.core.exceptions import UnauthorizedError

ALGORITHM = "HS256"


def require_signing_key(settings: Settings) -> None:
    """Refuse to serve HTTP with a signing key anybody could guess. Called from `create_app`.

    Here rather than in the Settings validator on purpose. Only a credential *every* process needs
    belongs in that validator, and the migration job is granted no signing key because it signs
    nothing — checking there would turn least privilege into a start-up crash. This runs when an
    application is built, which is every process that can actually issue a token.

    A deployed service that starts with the sentinel key issues sessions that anybody who has read
    this template can forge, and it fails silently, because everything works.
    """
    if not settings.is_deployed:
        return
    secret = settings.jwt_secret.get_secret_value()
    if secret == PLACEHOLDER_JWT_SECRET or len(secret) < MIN_JWT_SECRET_LENGTH:
        raise ValueError(
            "JWT_SECRET must be set outside local and test, and must be at least "
            f"{MIN_JWT_SECRET_LENGTH} characters"
        )


class TokenType(StrEnum):
    """What a token is for. Carried as `typ` and compared on every decode.

    One member today. It is here anyway: the day a refresh token is added, `typ` is what stops a
    long-lived refresh credential being presented as a bearer token on every guarded route — and a
    claim added later invalidates every token already in flight.
    """

    ACCESS = "access"


class TokenClaims(BaseModel):
    """The claims this service reads back out of its own tokens.

    `extra="ignore"`, deliberately: this is not a wire contract, and a claim added by a newer
    issuer must not lock out every token already signed.
    """

    model_config = ConfigDict(extra="ignore")

    sub: uuid.UUID
    jti: uuid.UUID
    typ: str
    iat: int
    exp: int


def issue_access_token(*, user_id: uuid.UUID) -> tuple[str, int]:
    """Mint an access token. Returns the token and its lifetime in seconds."""
    settings = get_settings()
    ttl = dt.timedelta(seconds=settings.access_token_ttl_seconds)
    token = _encode({"sub": str(user_id), "typ": TokenType.ACCESS.value}, ttl)
    return token, settings.access_token_ttl_seconds


def decode_token(token: str, *, expected: TokenType) -> TokenClaims:
    """Verify a token and return its claims. Raises `UnauthorizedError` for anything less.

    One error for absent, malformed, expired, wrong-issuer and wrong-type. Telling a caller which
    one it was tells an attacker which half of the guess was right.

    Four of the arguments below are a documented vulnerability class each if they are left out:

    * `algorithms` is an allowlist, never the algorithm the token names. A list holding both an
      HMAC and an RSA entry is the algorithm-confusion attack.
    * `require` is needed because PyJWT only checks `exp` when `exp` is present — a token minted
      without one otherwise verifies for ever.
    * `issuer` and `audience` are what stop a token from staging, or from another service sharing
      the key, being accepted here.
    * `typ` compared against `expected` is the one people miss.
    """
    settings = get_settings()
    try:
        payload = jwt.decode(
            token,
            settings.jwt_secret.get_secret_value(),
            algorithms=[ALGORITHM],
            issuer=settings.jwt_issuer,
            audience=settings.jwt_audience,
            options={"require": ["exp", "iat", "sub", "jti", "typ"]},
            # Clock skew between instances, not a grace period.
            leeway=10,
        )
    except jwt.InvalidTokenError as exc:
        raise UnauthorizedError("Invalid or expired token") from exc
    if payload.get("typ") != expected.value:
        raise UnauthorizedError("Invalid or expired token")
    try:
        return TokenClaims.model_validate(payload)
    except ValueError as exc:
        # A signed token whose claims are the wrong shape. Still a 401: it verified, so it came
        # from something holding the key, but it is not a token this service issued.
        raise UnauthorizedError("Invalid or expired token") from exc


def _encode(claims: dict[str, Any], ttl: dt.timedelta) -> str:
    settings = get_settings()
    now = dt.datetime.now(tz=dt.UTC)
    payload = {
        **claims,
        "iss": settings.jwt_issuer,
        "aud": settings.jwt_audience,
        "iat": now,
        "exp": now + ttl,
        # Unique per token. Nothing revokes an access token today — it lives fifteen minutes —
        # but a token that cannot be named cannot be denied later either.
        "jti": str(uuid.uuid4()),
    }
    return jwt.encode(payload, settings.jwt_secret.get_secret_value(), algorithm=ALGORITHM)
