"""The hashing module, on its own. No database, no app, no HTTP.

It is the only part of the auth path that can be tested like this, which is the reason it is
written first when you add one.
"""

from __future__ import annotations

from typing import Any

import pytest

from app.core.security import passwords
from app.core.security.passwords import hash_password, verify_password


async def test_a_digest_verifies_and_reveals_nothing() -> None:
    stored = await hash_password("correct horse battery staple")

    assert stored.startswith("$argon2id$")
    assert "correct horse" not in stored
    assert await verify_password("correct horse battery staple", stored) == (True, None)


async def test_verification_is_case_sensitive() -> None:
    stored = await hash_password("correct horse battery staple")

    matched, _ = await verify_password("Correct horse battery staple", stored)

    assert matched is False


async def test_the_same_password_hashes_differently_every_time() -> None:
    """Salted. Two users with the same password must not have the same row."""
    first = await hash_password("correct horse battery staple")
    second = await hash_password("correct horse battery staple")

    assert first != second


async def test_an_unknown_account_still_pays_for_a_verification(
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    """The property that stops this endpoint being a user-enumeration oracle.

    Asserted structurally rather than by timing: a wall-clock comparison is the flakiest test in
    any suite, and what actually matters is that the miss runs a real Argon2 verification at all.
    """
    calls: list[str] = []
    real_verify = passwords._hasher.verify

    def spy(password: Any, digest: Any) -> bool:
        calls.append(str(password))
        return bool(real_verify(password, digest))

    monkeypatch.setattr(passwords._hasher, "verify", spy)

    assert await verify_password("anything", None) == (False, None)
    assert calls == ["anything"]


async def test_a_digest_this_build_cannot_read_fails_the_login_not_the_request() -> None:
    matched, upgraded = await verify_password("anything", "not-a-hash")

    assert matched is False
    assert upgraded is None
