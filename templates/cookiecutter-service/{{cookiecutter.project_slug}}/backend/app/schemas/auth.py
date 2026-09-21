"""Authentication wire contracts. Typed in and typed out — no `dict[str, Any]` crosses a boundary.

The password bounds are here rather than in the service on purpose. The floor because short
passwords are guessable; the ceiling because Argon2 will cheerfully spend real CPU on a
ten-megabyte password, and an endpoint that lets an anonymous caller choose how much CPU to spend
is a denial of service with a login form on it.

There are no composition rules. They produce `Password1!` and a sticky note, and they rule out the
passphrases that are actually strong.
"""

from __future__ import annotations

import datetime as dt
import uuid
from typing import Annotated, Literal

from pydantic import AfterValidator, BaseModel, ConfigDict, Field, SecretStr

#: Long enough that Argon2's cost is doing work rather than covering for a four-character password.
MIN_PASSWORD_LENGTH = 12
MAX_PASSWORD_LENGTH = 1024


def _normalise_email(value: str) -> str:
    """Trim and lower-case, which is what the unique index on `users.email` assumes.

    Normalisation, not validation: this rejects a value with no `@` and nothing else. Two rows
    differing only in case is an account-takeover shape — whichever one a lookup finds first wins,
    and which one that is depends on the query plan — so the fix belongs at the boundary rather
    than in a case-insensitive comparison somebody later forgets to write.

    Add `pydantic[email]` and swap this for `EmailStr` if you want real address validation.
    """
    email = value.strip().lower()
    local, at, domain = email.partition("@")
    if not at or not local or not domain:
        raise ValueError("email must look like name@example.com")
    return email


Email = Annotated[
    str,
    Field(min_length=3, max_length=320, examples=["ada@example.com"]),
    AfterValidator(_normalise_email),
]

#: `SecretStr` is what makes an accidental `payload.model_dump()` in a log line print `**********`
#: instead of the password. It costs one `.get_secret_value()` at the single place that needs it.
Password = Annotated[
    SecretStr, Field(min_length=MIN_PASSWORD_LENGTH, max_length=MAX_PASSWORD_LENGTH)
]


class RegisterRequest(BaseModel):
    email: Email
    password: Password


class LoginRequest(BaseModel):
    email: Email
    password: Password


class ChangePasswordRequest(BaseModel):
    #: Not length-bounded: this one is checked against a stored digest, not hashed into a new one,
    #: and a bound here would tell a caller how long the real password is not.
    current_password: SecretStr
    new_password: Password


class UserRead(BaseModel):
    """What every endpoint that returns a user returns.

    There is no `hashed_password` field, and adding one is how a digest reaches a log aggregator,
    a browser's network tab and a support ticket in the same afternoon.
    """

    # Required for `model_validate` to accept an ORM instance. Without it the service fails at
    # runtime rather than at type-check time.
    model_config = ConfigDict(from_attributes=True)

    id: uuid.UUID
    email: str
    is_active: bool
    created_at: dt.datetime
    updated_at: dt.datetime


class AccessTokenRead(BaseModel):
    access_token: str
    #: The scheme the caller must send it back under: `Authorization: Bearer <token>`.
    token_type: Literal["bearer"] = "bearer"  # noqa: S105 - a scheme name, not a credential
    #: Sent even though the client could read `exp` out of the token, because a client that has to
    #: decode a JWT to know when to re-authenticate is a client that will decode it without
    #: verifying it.
    expires_in: int
