"""The HTTP contract, through the full stack: route -> service -> repository -> Postgres.

Nothing is mocked, and `get_current_actor` is never overridden here. A suite that overrides the
actor dependency everywhere has stopped testing the decoder, and the decoder is the part with the
vulnerability classes in it.
"""

from __future__ import annotations

import uuid
from typing import Any

import pytest
from httpx import AsyncClient

from app.core.config import get_settings
from app.core.security.tokens import issue_access_token
from tests.helpers import NEW_PASSWORD, PASSWORD, register_payload


async def _registered(client: AsyncClient, **overrides: Any) -> tuple[dict[str, Any], str]:
    """Register, log in, and hand back the user and a bearer token for it."""
    payload = register_payload(**overrides)
    created = await client.post("/api/v1/auth/register", json=payload)
    assert created.status_code == 201
    login = await client.post(
        "/api/v1/auth/login",
        json={"email": payload["email"], "password": payload["password"]},
    )
    assert login.status_code == 200
    return payload, str(login.json()["access_token"])


def _auth(token: str) -> dict[str, str]:
    return {"Authorization": f"Bearer {token}"}


# --- Register -------------------------------------------------------------


async def test_register_returns_the_user_and_never_the_digest(client: AsyncClient) -> None:
    payload = register_payload()

    response = await client.post("/api/v1/auth/register", json=payload)

    assert response.status_code == 201
    body = response.json()
    assert body["email"] == payload["email"]
    assert body["is_active"] is True
    assert uuid.UUID(body["id"])
    assert "hashed_password" not in body
    assert "argon2" not in response.text
    assert payload["password"] not in response.text


async def test_a_duplicate_email_is_refused_in_the_documented_shape(client: AsyncClient) -> None:
    payload = register_payload()
    await client.post("/api/v1/auth/register", json=payload)

    response = await client.post("/api/v1/auth/register", json=payload)

    assert response.status_code == 409
    # One error contract for the whole API — see app/schemas/common.py.
    assert response.json()["error"]["code"] == "conflict"


@pytest.mark.parametrize(
    ("overrides", "expected_status"),
    [
        ({"password": "short"}, 422),
        ({"password": "x" * 1025}, 422),
        ({"email": "not-an-email"}, 422),
        ({"email": ""}, 422),
        ({}, 201),
    ],
    ids=["password-too-short", "password-too-long", "email-has-no-at", "email-empty", "valid"],
)
async def test_registration_validation(
    client: AsyncClient, overrides: dict[str, Any], expected_status: int
) -> None:
    response = await client.post("/api/v1/auth/register", json=register_payload(**overrides))

    assert response.status_code == expected_status


# --- Login ----------------------------------------------------------------


async def test_login_returns_a_usable_bearer_token(client: AsyncClient) -> None:
    payload, token = await _registered(client)

    me = await client.get("/api/v1/auth/me", headers=_auth(token))

    assert me.status_code == 200
    assert me.json()["email"] == payload["email"]


async def test_login_says_how_long_the_token_lives(client: AsyncClient) -> None:
    payload = register_payload()
    await client.post("/api/v1/auth/register", json=payload)

    response = await client.post(
        "/api/v1/auth/login",
        json={"email": payload["email"], "password": payload["password"]},
    )

    body = response.json()
    assert body["token_type"] == "bearer"
    assert body["expires_in"] == get_settings().access_token_ttl_seconds


async def test_an_unknown_email_and_a_wrong_password_are_indistinguishable(
    client: AsyncClient,
) -> None:
    """Same status, same body. Anything else turns this endpoint into a user directory."""
    payload = register_payload()
    await client.post("/api/v1/auth/register", json=payload)

    wrong_password = await client.post(
        "/api/v1/auth/login", json={"email": payload["email"], "password": NEW_PASSWORD}
    )
    unknown_email = await client.post(
        "/api/v1/auth/login",
        json={"email": f"nobody-{uuid.uuid4().hex[:8]}@example.com", "password": PASSWORD},
    )

    assert wrong_password.status_code == unknown_email.status_code == 401
    assert wrong_password.json() == unknown_email.json()
    assert wrong_password.json()["error"]["code"] == "unauthorized"


async def test_a_failed_login_leaks_no_digest(client: AsyncClient) -> None:
    payload = register_payload()
    await client.post("/api/v1/auth/register", json=payload)

    response = await client.post(
        "/api/v1/auth/login", json={"email": payload["email"], "password": NEW_PASSWORD}
    )

    assert "argon2" not in response.text
    assert payload["email"] not in response.text


# --- Me -------------------------------------------------------------------


async def test_me_without_a_token_is_401_and_says_which_scheme(client: AsyncClient) -> None:
    """401 and not 403, with `WWW-Authenticate` — or a client will not try to authenticate."""
    response = await client.get("/api/v1/auth/me")

    assert response.status_code == 401
    assert response.headers["www-authenticate"] == "Bearer"
    assert response.json()["error"]["code"] == "unauthorized"


@pytest.mark.parametrize(
    "header",
    [
        {"Authorization": "Bearer not-a-token"},
        {"Authorization": "Basic YWRhOnNlY3JldA=="},
        {"Authorization": "Bearer "},
    ],
    ids=["garbage-token", "wrong-scheme", "empty-token"],
)
async def test_me_with_a_credential_that_is_not_one_is_401(
    client: AsyncClient, header: dict[str, str]
) -> None:
    response = await client.get("/api/v1/auth/me", headers=header)

    assert response.status_code == 401


async def test_me_with_a_tampered_token_is_401(client: AsyncClient) -> None:
    _, token = await _registered(client)
    head, _, signature = token.rpartition(".")
    flipped = "A" if signature[0] != "A" else "B"
    tampered = f"{head}.{flipped}{signature[1:]}"

    response = await client.get("/api/v1/auth/me", headers=_auth(tampered))

    assert response.status_code == 401


async def test_me_with_an_expired_token_is_401(
    client: AsyncClient, monkeypatch: pytest.MonkeyPatch
) -> None:
    _, token = await _registered(client)
    caller = await client.get("/api/v1/auth/me", headers=_auth(token))
    # Minted through the real encoder with the lifetime turned negative, so this proves the route
    # rejects an expired token rather than proving the test can craft a bad one.
    monkeypatch.setenv("ACCESS_TOKEN_TTL_SECONDS", "-60")
    get_settings.cache_clear()
    expired, _ = issue_access_token(user_id=uuid.UUID(caller.json()["id"]))

    response = await client.get("/api/v1/auth/me", headers=_auth(expired))

    assert response.status_code == 401


async def test_me_never_includes_the_digest(client: AsyncClient) -> None:
    _, token = await _registered(client)

    response = await client.get("/api/v1/auth/me", headers=_auth(token))

    assert "hashed_password" not in response.json()
    assert "argon2" not in response.text


# --- Change password ------------------------------------------------------


async def test_changing_a_password_retires_the_old_one(client: AsyncClient) -> None:
    payload, token = await _registered(client)

    changed = await client.post(
        "/api/v1/auth/change-password",
        headers=_auth(token),
        json={"current_password": payload["password"], "new_password": NEW_PASSWORD},
    )

    assert changed.status_code == 204
    with_old = await client.post(
        "/api/v1/auth/login", json={"email": payload["email"], "password": payload["password"]}
    )
    assert with_old.status_code == 401
    with_new = await client.post(
        "/api/v1/auth/login", json={"email": payload["email"], "password": NEW_PASSWORD}
    )
    assert with_new.status_code == 200


async def test_changing_a_password_needs_the_current_one(client: AsyncClient) -> None:
    _, token = await _registered(client)

    response = await client.post(
        "/api/v1/auth/change-password",
        headers=_auth(token),
        json={"current_password": NEW_PASSWORD, "new_password": NEW_PASSWORD},
    )

    assert response.status_code == 403
    assert response.json()["error"]["code"] == "forbidden"


async def test_changing_a_password_without_a_token_is_401(client: AsyncClient) -> None:
    response = await client.post(
        "/api/v1/auth/change-password",
        json={"current_password": PASSWORD, "new_password": NEW_PASSWORD},
    )

    assert response.status_code == 401
