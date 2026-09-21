"""The authentication use cases, and every rule they apply. The only place either lives.

A public method here is a whole use case: it applies the rules, calls whatever the use case needs,
and returns the wire schema the router sends back. Ask Athena for the layered architecture rules.

Raises `AppError` subclasses, never `HTTPException`: this service must be callable from a worker,
a CLI or a test with no HTTP stack anywhere.

What this service does **not** do, so that nobody assumes it does:

* No refresh tokens, and therefore no logout and no revocation. An access token is valid until it
  expires — changing a password does not end a session that is already open, and deactivating a
  user does not either. Ask Athena for the JWT authentication guide; rotation and revocation are a
  step of it, and they need a table of their own.
* No roles and no permissions. Every authenticated caller may do everything on this surface, which
  is true only because the surface is four endpoints about the caller's own account. The first
  endpoint that touches somebody else's row needs the authorization rules before it needs code.
* No password reset and no email verification. Both need to send mail, which is a dependency this
  template does not choose for you.
"""

from __future__ import annotations

import uuid

import structlog

from app.core.exceptions import ConflictError, ForbiddenError, UnauthorizedError
from app.core.security.passwords import hash_password, verify_password
from app.core.security.tokens import issue_access_token
from app.models.user import User
from app.repositories.user_repository import UserRepository
from app.schemas.auth import (
    AccessTokenRead,
    ChangePasswordRequest,
    LoginRequest,
    RegisterRequest,
    UserRead,
)

logger = structlog.get_logger(__name__)


class AuthService:
    def __init__(self, users: UserRepository) -> None:
        self._users = users

    async def register(self, payload: RegisterRequest) -> UserRead:
        """Create an account. Returns the user, not a session — the caller logs in next.

        This does leak that an address is registered. The alternative, answering 201 to a
        duplicate, means the real owner gets no account while the caller believes they have one;
        and every registration form on the internet leaks the same thing through its error message
        anyway. Login, where it actually matters, does not distinguish.
        """
        if await self._users.get_by_email(payload.email) is not None:
            raise ConflictError("An account with that email already exists")
        user = await self._users.create(
            email=payload.email,
            hashed_password=await hash_password(payload.password.get_secret_value()),
        )
        # The id, never the email and never the digest: a log line is read by more people than a
        # database is.
        logger.info("user_registered", user_id=str(user.id))
        return UserRead.model_validate(user)

    async def login(self, payload: LoginRequest) -> AccessTokenRead:
        """Verify a password and mint an access token.

        Three different failures — no such email, wrong password, deactivated account — answer
        with one message, one status and one shape. Telling them apart is information the caller
        has not authenticated well enough to receive, and "this account is disabled" confirms that
        the account exists.

        The verification runs before any branch on `user`, so a miss costs what a hit costs. A
        response that is fast for unknown addresses and slow for known ones is an enumeration
        oracle no matter how identical the body is.
        """
        user = await self._users.get_by_email(payload.email)
        matched, upgraded = await verify_password(
            payload.password.get_secret_value(),
            user.hashed_password if user is not None else None,
        )
        if user is None or not matched or not user.is_active:
            raise UnauthorizedError("Invalid email or password")
        if upgraded is not None:
            # The one write on the login path, and it is conditional: pwdlib returns a new digest
            # only when the stored one was made with weaker parameters than the current
            # recommendation. Login is the only moment the plaintext is in hand.
            await self._users.set_password_hash(user, upgraded)
        token, expires_in = issue_access_token(user_id=user.id)
        logger.info("user_logged_in", user_id=str(user.id))
        return AccessTokenRead(access_token=token, expires_in=expires_in)

    async def me(self, user_id: uuid.UUID) -> UserRead:
        """The signed-in user, read from the database rather than from the token's claims.

        The token says who the caller is; it does not say what is currently true about them. A
        user deactivated four minutes ago still holds a valid access token, and this is the
        endpoint where that must not read as "fine".
        """
        return UserRead.model_validate(await self._active_user(user_id))

    async def change_password(self, user_id: uuid.UUID, payload: ChangePasswordRequest) -> None:
        """Replace a password, proving knowledge of the current one first.

        A valid access token is not enough. It is a bearer credential that may have been copied
        out of a log or a browser, and a password change is the one operation that would lock the
        real owner out.

        The caller's existing token keeps working until it expires — there is nothing here to
        revoke, because this template issues no refresh tokens and stores no sessions. Ending
        every other session on a password change is exactly what a refresh-session table buys, and
        it is the next step of the handbook's JWT guide rather than something to fake here.
        """
        user = await self._active_user(user_id)
        matched, _ = await verify_password(
            payload.current_password.get_secret_value(), user.hashed_password
        )
        if not matched:
            # Not 401: the session is valid and a client that saw one would try to re-authenticate
            # rather than tell the user their current password was wrong. Not 422 either — the
            # request was well formed. The caller simply has not earned this operation.
            raise ForbiddenError("Current password is incorrect")
        await self._users.set_password_hash(
            user, await hash_password(payload.new_password.get_secret_value())
        )
        logger.info("user_changed_password", user_id=str(user.id))

    async def _active_user(self, user_id: uuid.UUID) -> User:
        """The row behind a verified token, or a 401. The service's internal surface.

        Not a 404: the caller is holding a signed token for a user that no longer exists or has
        been deactivated, and that is a stale credential rather than a missing resource.

        Underscored because only this service calls it today. A second service needing the row
        gets a public method with the same body; a router never gets one either way, because that
        would put an ORM instance in the view layer.
        """
        user = await self._users.get(user_id)
        if user is None or not user.is_active:
            raise UnauthorizedError("Invalid or expired token")
        return user
