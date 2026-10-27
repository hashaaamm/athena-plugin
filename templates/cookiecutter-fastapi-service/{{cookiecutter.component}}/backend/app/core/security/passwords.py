"""Password hashing. Argon2id, off the event loop, and the same cost whether the account exists.

This module depends on nothing else in the service, which is why it is the one piece of the auth
path with a unit test that needs no database and no app.

Three decisions live in the twenty lines below, and each of them is a failure somebody has shipped.

**Argon2id through pwdlib, never bcrypt through passlib.** Ask Athena which auth libraries are
approved before substituting one: passlib's bcrypt handler breaks on bcrypt 4.x and *logs* the
failure instead of raising it, and bcrypt truncates the input at 72 bytes, so a passphrase manager
produces users for whom the tail of the password is decoration.

**It runs in a thread.** Argon2 at pwdlib's recommended parameters costs tens of milliseconds of
CPU, on purpose — the cost is the defence. Paid directly inside an `async def`, it is paid by the
whole worker: nothing else on that event loop runs for the duration, including the readiness probe,
and a burst of logins makes an instance that is merely busy look dead to the platform.

**A miss costs what a hit costs.** See `verify_password`.
"""

from __future__ import annotations

import secrets
from functools import lru_cache

from anyio import to_thread
from pwdlib import PasswordHash
from pwdlib.exceptions import UnknownHashError

#: Argon2id at pwdlib's recommended parameters. One place for the cost factors, which is what
#: makes raising them a one-line change — and `verify_and_update` below is what migrates the
#: hashes already in the table when they are raised.
_hasher = PasswordHash.recommended()


@lru_cache(maxsize=1)
def _absent_user_hash() -> str:
    """A real digest of a value no account has, computed once and never at import.

    Lazy because the migration job imports this package and must not pay for an Argon2 hash it
    will never verify against.
    """
    return _hasher.hash(secrets.token_urlsafe(32))


async def hash_password(raw: str) -> str:
    """The digest to store. Salt and parameters are part of the returned string."""
    return await to_thread.run_sync(_hasher.hash, raw)


async def verify_password(raw: str, stored: str | None) -> tuple[bool, str | None]:
    """Check a password. Returns `(matched, upgraded_hash)`.

    `stored=None` means there is no such account — and the CPU is burned anyway. Returning early
    on a miss answers in microseconds where a hit pays Argon2's full cost, and that difference is
    stable enough to enumerate a user base through a load balancer over a lunch break. The caller
    must also answer with the same body and the same status for both; `AuthService.login` does.

    `upgraded_hash` is not `None` when the stored digest was made with weaker parameters than the
    current recommendation. Login is the only moment the plaintext is in hand, so it is the only
    moment the digest can be migrated. The caller persists it, which makes that write conditional
    rather than a write on every login.
    """
    if stored is None:
        await to_thread.run_sync(_hasher.verify, raw, _absent_user_hash())
        return False, None
    try:
        matched, upgraded = await to_thread.run_sync(_hasher.verify_and_update, raw, stored)
    except UnknownHashError:
        # A digest this build cannot identify — written by a hasher that has since been removed,
        # or a column somebody edited by hand. It must fail the login, not the request: a 500 here
        # turns one broken row into a page.
        return False, None
    return matched, upgraded
