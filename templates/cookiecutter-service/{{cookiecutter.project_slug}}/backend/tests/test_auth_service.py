"""Service rules and service output, tested where they live.

These go through a real repository and a real Postgres: the uniqueness rule is only interesting
because the database also enforces it, and a mocked repository would assert nothing about that.

The service is also the entry point, so what it hands back is part of its contract — a use-case
method returns a wire schema, never an ORM row. `test_use_case_methods_return_wire_schemas` is the
test that fails when somebody returns the entity because it was right there.
"""

from __future__ import annotations

import datetime as dt
import uuid

import pytest
from sqlalchemy import select
from sqlalchemy.ext.asyncio import AsyncSession

from app.core.exceptions import (
    CompromisedSessionError,
    ConflictError,
    ForbiddenError,
    UnauthorizedError,
)
from app.core.security.tokens import TokenType, decode_token
from app.models.refresh_session import RefreshSession
from app.models.user import User
from app.repositories.refresh_session_repository import RefreshSessionRepository
from app.repositories.user_repository import UserRepository
from app.schemas.auth import (
    ChangePasswordRequest,
    LoginRequest,
    RegisterRequest,
    TokenPairRead,
    UserRead,
)
from app.services.auth_service import AuthService
from tests.helpers import NEW_PASSWORD, PASSWORD


@pytest.fixture
def service(session: AsyncSession) -> AuthService:
    return AuthService(UserRepository(session), RefreshSessionRepository(session))


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
        TokenPairRead,
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


# --- Sessions -------------------------------------------------------------


async def _login(service: AuthService) -> TokenPairRead:
    return await service.login(LoginRequest(email="ada@example.com", password=PASSWORD))


async def _row(session: AsyncSession, token: str) -> RefreshSession:
    row = await session.get(RefreshSession, decode_token(token, expected=TokenType.REFRESH).jti)
    assert row is not None
    return row


async def test_login_records_the_refresh_token_by_jti_and_never_the_token(
    service: AuthService, session: AsyncSession
) -> None:
    user = await _register(service)

    pair = await _login(service)

    row = await _row(session, pair.refresh_token)
    assert row.user_id == user.id
    assert row.revoked_at is None
    assert row.expires_at > dt.datetime.now(tz=dt.UTC)
    stored = (await session.execute(select(RefreshSession.__table__))).all()
    assert pair.refresh_token not in repr(stored)


async def test_rotation_revokes_the_spent_row_and_continues_the_family(
    service: AuthService, session: AsyncSession
) -> None:
    await _register(service)
    first = await _login(service)

    second = await service.refresh(first.refresh_token)

    spent = await _row(session, first.refresh_token)
    live = await _row(session, second.refresh_token)
    assert spent.revoked_at is not None
    assert live.revoked_at is None
    assert live.family_id == spent.family_id
    assert live.id != spent.id


async def test_each_login_is_a_family_of_its_own(
    service: AuthService, session: AsyncSession
) -> None:
    await _register(service)

    here, there = await _login(service), await _login(service)

    assert (await _row(session, here.refresh_token)).family_id != (
        await _row(session, there.refresh_token)
    ).family_id


async def test_a_replay_is_refused_with_the_error_that_keeps_the_revocation(
    service: AuthService, session: AsyncSession
) -> None:
    """`CompromisedSessionError` is what tells the request's transaction to commit on the way to
    a 401. A plain `UnauthorizedError` here would be rolled back along with the revocation."""
    await _register(service)
    first = await _login(service)
    second = await service.refresh(first.refresh_token)

    with pytest.raises(CompromisedSessionError):
        await service.refresh(first.refresh_token)

    assert (await _row(session, second.refresh_token)).revoked_at is not None


async def test_a_deactivated_user_cannot_refresh_and_loses_the_family(
    service: AuthService, session: AsyncSession
) -> None:
    """Deactivation stops new access tokens at once. The one already issued runs out its fifteen
    minutes — that is the revocation latency, and it is documented rather than hidden."""
    user = await _register(service)
    pair = await _login(service)
    row = await session.get(User, user.id)
    assert row is not None
    row.is_active = False
    await session.flush()

    with pytest.raises(CompromisedSessionError):
        await service.refresh(pair.refresh_token)

    assert (await _row(session, pair.refresh_token)).revoked_at is not None


async def test_a_row_past_its_expiry_is_refused_even_if_the_token_is_not(
    service: AuthService, session: AsyncSession
) -> None:
    """The row is the authority. Shortening a session is an UPDATE, not a re-issue."""
    await _register(service)
    pair = await _login(service)
    row = await _row(session, pair.refresh_token)
    row.expires_at = dt.datetime.now(tz=dt.UTC) - dt.timedelta(seconds=1)
    await session.flush()

    with pytest.raises(UnauthorizedError):
        await service.refresh(pair.refresh_token)


async def test_refreshing_with_no_token_is_refused(service: AuthService) -> None:
    with pytest.raises(UnauthorizedError):
        await service.refresh(None)


async def test_logout_revokes_the_family_and_tolerates_anything(
    service: AuthService, session: AsyncSession
) -> None:
    await _register(service)
    pair = await _login(service)

    await service.logout(pair.refresh_token)
    await service.logout(pair.refresh_token)
    await service.logout("not-a-token")
    await service.logout(None)

    assert (await _row(session, pair.refresh_token)).revoked_at is not None


async def test_changing_a_password_revokes_every_family_and_returns_a_new_one(
    service: AuthService, session: AsyncSession
) -> None:
    user = await _register(service)
    here, there = await _login(service), await _login(service)

    fresh = await service.change_password(
        user.id,
        ChangePasswordRequest(current_password=PASSWORD, new_password=NEW_PASSWORD),
    )

    assert isinstance(fresh, TokenPairRead)
    assert (await _row(session, here.refresh_token)).revoked_at is not None
    assert (await _row(session, there.refresh_token)).revoked_at is not None
    new_row = await _row(session, fresh.refresh_token)
    assert new_row.revoked_at is None
    assert new_row.family_id not in {
        (await _row(session, here.refresh_token)).family_id,
        (await _row(session, there.refresh_token)).family_id,
    }
