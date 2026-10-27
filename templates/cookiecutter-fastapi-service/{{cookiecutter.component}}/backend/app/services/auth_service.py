"""The authentication use cases, and every rule they apply. The only place either lives.

A public method here is a whole use case: it applies the rules, calls whatever the use case needs,
and returns the wire schema the router sends back. Ask Athena for the layered architecture rules.

Raises `AppError` subclasses, never `HTTPException`: this service must be callable from a worker,
a CLI or a test with no HTTP stack anywhere.

**Sessions.** Login starts one: an access token and a refresh token, with a `refresh_sessions` row
keyed by the refresh token's `jti` and a new `family_id`. Refresh spends the refresh token and
returns the next pair in the same family; a spent token presented again revokes the whole family.
Logout revokes the family. Changing a password revokes every family the user has and starts a new
one for the caller. Where the refresh token travels — a JSON body or an httpOnly cookie — is the
router's business; nothing here knows.

**Revocation latency is fifteen minutes**, and it is a number to publish rather than discover.
Revoking a session stops it minting access tokens at once; the access token it already minted is
checked against nothing and keeps working until it expires. Deactivating a user is the same
sentence — `refresh` refuses an inactive user and revokes the family it was asked to extend. A
compliance position that cannot accept fifteen minutes needs an opaque access token and a lookup
per request, which is the cost a JWT was chosen to avoid.

What this service does **not** do, so that nobody assumes it does:

* No roles and no permissions. Every authenticated caller may do everything on this surface, which
  is true only because the surface is about the caller's own account. The first endpoint that
  touches somebody else's row needs the authorization rules before it needs code.
* No password reset and no email verification. Both need to send mail, which is a dependency this
  template does not choose for you.
* No grace window on rotation. Two refreshes racing with the same token look exactly like a
  replay, so a client that fires them in parallel logs its user out. The fix is one in-flight
  refresh per client, not a window in which a stolen token is also accepted.
"""

from __future__ import annotations

import datetime as dt
import uuid

import structlog

from app.core.exceptions import (
    CompromisedSessionError,
    ConflictError,
    ForbiddenError,
    UnauthorizedError,
)
from app.core.security.passwords import hash_password, verify_password
from app.core.security.tokens import (
    TokenType,
    decode_token,
    issue_access_token,
    issue_refresh_token,
)
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

logger = structlog.get_logger(__name__)

#: One message for every refused refresh token — malformed, expired, spent, revoked, or naming a
#: user who can no longer sign in. Which check failed is information for the log, not the caller.
_REFUSED = "Invalid or expired token"


class AuthService:
    def __init__(self, users: UserRepository, sessions: RefreshSessionRepository) -> None:
        self._users = users
        self._sessions = sessions

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

    async def login(self, payload: LoginRequest) -> TokenPairRead:
        """Verify a password and start a session: a new family, and its first token pair.

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
            # pwdlib returns a new digest only when the stored one was made with weaker parameters
            # than the current recommendation. Login is the only moment the plaintext is in hand.
            await self._users.set_password_hash(user, upgraded)
        pair = await self._issue_pair(user.id, family_id=uuid.uuid4())
        logger.info("user_logged_in", user_id=str(user.id))
        return pair

    async def refresh(self, refresh_token: str | None) -> TokenPairRead:
        """Spend a refresh token for the next pair in its family. `None` means none was presented.

        The row is read under a lock, so two refreshes racing with one token cannot both win: the
        second waits for the first, then finds the row revoked. That is indistinguishable from a
        replay, and it is treated as one.

        A revoked row presented again is the case the table exists for. The token verified, so this
        service issued it; it has been spent once already, so somebody else holds a copy — a replay,
        or a stolen token racing the real client. Either way the family is compromised and all of
        it is revoked. `CompromisedSessionError` is what keeps that revocation when the request
        answers 401; any other error would roll it back.
        """
        if refresh_token is None:
            raise UnauthorizedError(_REFUSED)
        claims = decode_token(refresh_token, expected=TokenType.REFRESH)
        now = dt.datetime.now(tz=dt.UTC)
        session = await self._sessions.get_for_update(claims.jti)
        if session is None or session.user_id != claims.sub or session.expires_at <= now:
            raise UnauthorizedError(_REFUSED)
        if session.revoked_at is not None:
            await self._sessions.revoke_family(session.family_id, at=now)
            # Warning, not info: this is the event an incident starts from. Ids only, never the
            # token — a log line is read by more people than a database is.
            logger.warning(
                "refresh_token_reused",
                user_id=str(session.user_id),
                family_id=str(session.family_id),
            )
            raise CompromisedSessionError(_REFUSED)
        user = await self._users.get(session.user_id)
        if user is None or not user.is_active:
            # A user deactivated since this family began. Nothing more is minted for it, and the
            # family goes so the next attempt is refused without reaching this branch.
            await self._sessions.revoke_family(session.family_id, at=now)
            raise CompromisedSessionError(_REFUSED)
        return await self._issue_pair(user.id, family_id=session.family_id, replacing=session)

    async def logout(self, refresh_token: str | None) -> None:
        """End the session a refresh token belongs to: its whole family, on this device.

        Never an error. Clients retry logout on a flaky network, and a 401 from it produces a
        support ticket about being unable to sign out. A token that does not decode — absent,
        expired, malformed — names no session that could still be extended, so there is nothing
        to end and the answer is the same. The access token the client holds is not revoked; it
        expires on its own, which is the revocation latency in this module's docstring.
        """
        if refresh_token is None:
            return
        try:
            claims = decode_token(refresh_token, expected=TokenType.REFRESH)
        except UnauthorizedError:
            return
        # Locked for the same reason `refresh` locks it. A refresh racing this logout either
        # finishes first — and the revocation below, starting after it commits, sees the row it
        # inserted — or waits, and then finds its token revoked.
        session = await self._sessions.get_for_update(claims.jti)
        if session is None or session.user_id != claims.sub:
            return
        await self._sessions.revoke_family(session.family_id, at=dt.datetime.now(tz=dt.UTC))
        logger.info("user_logged_out", user_id=str(session.user_id))

    async def me(self, user_id: uuid.UUID) -> UserRead:
        """The signed-in user, read from the database rather than from the token's claims.

        The token says who the caller is; it does not say what is currently true about them. A
        user deactivated four minutes ago still holds a valid access token, and this is the
        endpoint where that must not read as "fine".
        """
        return UserRead.model_validate(await self._active_user(user_id))

    async def change_password(
        self, user_id: uuid.UUID, payload: ChangePasswordRequest
    ) -> TokenPairRead:
        """Replace a password, end every session the user has, and start a new one for the caller.

        A valid access token is not enough. It is a bearer credential that may have been copied
        out of a log or a browser, and a password change is the one operation that would lock the
        real owner out.

        Every family is revoked, the caller's own included. Otherwise "change your password" does
        not end the session of whoever knew the old one — and a stolen refresh token from the
        caller's own device is in the caller's family. The caller is handed a fresh pair in a new
        family, so the person who just proved they know the password stays signed in and nobody
        else does. Access tokens already issued still run out their fifteen minutes.
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
        await self._sessions.revoke_all_for_user(user.id, at=dt.datetime.now(tz=dt.UTC))
        pair = await self._issue_pair(user.id, family_id=uuid.uuid4())
        logger.info("user_changed_password", user_id=str(user.id))
        return pair

    async def _issue_pair(
        self,
        user_id: uuid.UUID,
        *,
        family_id: uuid.UUID,
        replacing: RefreshSession | None = None,
    ) -> TokenPairRead:
        """Mint an access token and a refresh token, and record the refresh token's row.

        When `replacing` is given its row is revoked in the same transaction as the new row is
        inserted. Committed separately, a crash between the two would leave the client holding a
        spent token and no new one — signed out, with nothing to show for it.
        """
        if replacing is not None:
            await self._sessions.revoke(replacing, at=dt.datetime.now(tz=dt.UTC))
        access_token, expires_in = issue_access_token(user_id=user_id)
        refresh = issue_refresh_token(user_id=user_id)
        await self._sessions.create(
            jti=refresh.jti,
            user_id=user_id,
            family_id=family_id,
            expires_at=refresh.expires_at,
        )
        return TokenPairRead(
            access_token=access_token,
            refresh_token=refresh.token,
            expires_in=expires_in,
        )

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
