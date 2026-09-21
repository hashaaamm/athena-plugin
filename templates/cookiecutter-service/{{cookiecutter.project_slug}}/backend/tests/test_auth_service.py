"""Service rules and service output, tested where they live.

These go through a real repository and a real Postgres: the uniqueness rule is only interesting
because the database also enforces it, and a mocked repository would assert nothing about that.

The service is also the entry point, so what it hands back is part of its contract — a use-case
method returns a wire schema, never an ORM row. `test_use_case_methods_return_wire_schemas` is the
test that fails when somebody returns the entity because it was right there.
"""

from __future__ import annotations

import uuid

import pytest
from sqlalchemy.ext.asyncio import AsyncSession

from app.core.exceptions import ConflictError, ForbiddenError, UnauthorizedError
from app.models.user import User
from app.repositories.user_repository import UserRepository
from app.schemas.auth import (
    AccessTokenRead,
    ChangePasswordRequest,
    LoginRequest,
    RegisterRequest,
    UserRead,
)
from app.services.auth_service import AuthService
from tests.helpers import NEW_PASSWORD, PASSWORD


@pytest.fixture
def service(session: AsyncSession) -> AuthService:
    return AuthService(UserRepository(session))


async def _register(service: AuthService, email: str = "ada@example.com") -> UserRead:
    return await service.register(RegisterRequest(email=email, password=PASSWORD))


async def test_register_persists_a_user_with_a_digest_not_a_password(
    service: AuthService, session: AsyncSession
) -> None:
    user = await _register(service)

    row = await session.get(User, user.id)
    assert row is not None
    assert row.hashed_password.startswith("$argon2id$")
    assert PASSWORD not in row.hashed_password


async def test_register_normalises_the_email(service: AuthService) -> None:
    """Two rows differing only in case is an account-takeover shape."""
    user = await _register(service, email="  Ada@Example.COM  ")

    assert user.email == "ada@example.com"
    with pytest.raises(ConflictError):
        await _register(service, email="ADA@example.com")


async def test_use_case_methods_return_wire_schemas(service: AuthService) -> None:
    created = await _register(service)

    assert isinstance(created, UserRead)
    assert not isinstance(created, User)
    assert isinstance(await service.me(created.id), UserRead)
    assert isinstance(
        await service.login(LoginRequest(email="ada@example.com", password=PASSWORD)),
        AccessTokenRead,
    )


async def test_a_duplicate_email_is_a_conflict(service: AuthService) -> None:
    await _register(service)

    with pytest.raises(ConflictError):
        await _register(service)


async def test_login_refuses_an_unknown_email_and_a_wrong_password_identically(
    service: AuthService,
) -> None:
    await _register(service)

    with pytest.raises(UnauthorizedError) as unknown:
        await service.login(LoginRequest(email="nobody@example.com", password=PASSWORD))
    with pytest.raises(UnauthorizedError) as wrong:
        await service.login(LoginRequest(email="ada@example.com", password=NEW_PASSWORD))

    assert str(unknown.value) == str(wrong.value)
    assert unknown.value.code == wrong.value.code


async def test_a_deactivated_user_cannot_log_in_and_is_not_told_why(
    service: AuthService, session: AsyncSession
) -> None:
    user = await _register(service)
    row = await session.get(User, user.id)
    assert row is not None
    row.is_active = False
    await session.flush()

    with pytest.raises(UnauthorizedError) as deactivated:
        await service.login(LoginRequest(email="ada@example.com", password=PASSWORD))

    assert str(deactivated.value) == "Invalid email or password"


async def test_me_refuses_a_token_for_a_user_that_is_gone(service: AuthService) -> None:
    """A signed token naming nobody is a stale credential, not a missing resource."""
    with pytest.raises(UnauthorizedError):
        await service.me(uuid.uuid4())


async def test_changing_a_password_retires_the_old_one(
    service: AuthService, session: AsyncSession
) -> None:
    user = await _register(service)
    before = await session.get(User, user.id)
    assert before is not None
    old_digest = before.hashed_password

    await service.change_password(
        user.id,
        ChangePasswordRequest(current_password=PASSWORD, new_password=NEW_PASSWORD),
    )

    after = await session.get(User, user.id)
    assert after is not None
    assert after.hashed_password != old_digest
    await service.login(LoginRequest(email="ada@example.com", password=NEW_PASSWORD))
    with pytest.raises(UnauthorizedError):
        await service.login(LoginRequest(email="ada@example.com", password=PASSWORD))


async def test_changing_a_password_needs_the_current_one(service: AuthService) -> None:
    user = await _register(service)

    with pytest.raises(ForbiddenError):
        await service.change_password(
            user.id,
            ChangePasswordRequest(current_password=NEW_PASSWORD, new_password=NEW_PASSWORD),
        )
