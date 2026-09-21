"""The caller, resolved from the bearer token. The only place in the service that decides 401.

This is the inverse of `tokens.py`. Everything downstream of it receives a caller that has already
been verified, or never runs at all.
"""

from __future__ import annotations

import uuid
from typing import Annotated

from fastapi import Depends
from fastapi.security import HTTPAuthorizationCredentials, HTTPBearer
from pydantic import BaseModel, ConfigDict

from app.core.exceptions import UnauthorizedError
from app.core.security.tokens import TokenType, decode_token

# `auto_error=False`, and it matters: `HTTPBearer(auto_error=True)` raises FastAPI's own
# HTTPException with status **403** when the Authorization header is missing — not 401, and not in
# this service's error shape. A generated client then reads a missing credential as a permissions
# problem and never tries to authenticate.
_bearer = HTTPBearer(auto_error=False)


class Actor(BaseModel):
    """Who is calling, rebuilt from the access token's claims. Not an ORM object.

    Nothing was loaded to produce this, which is the property that makes a JWT worth having and is
    also its cost: a deactivated account keeps calling until its token expires. Fifteen minutes,
    with the default lifetime in `app/core/config.py`. An endpoint that must not tolerate that
    window loads the row itself — `/auth/me` does, and says why.

    `frozen=True` is not decoration. It means a service cannot resolve an authorization problem by
    adding a field to the caller, which is a thing that happens under deadline and which no
    reviewer catches in a diff. Roles and permissions belong on this model when they arrive; ask
    Athena for the authorization and RBAC rules before inventing a shape for them.
    """

    model_config = ConfigDict(frozen=True)

    id: uuid.UUID


async def get_current_actor(
    credentials: Annotated[HTTPAuthorizationCredentials | None, Depends(_bearer)],
) -> Actor:
    if credentials is None or credentials.scheme.lower() != "bearer":
        raise UnauthorizedError("Authentication required")
    # `credentials.credentials` is the token alone. Passing the whole header here is the mistake
    # that produces "Not enough segments" from the decoder.
    claims = decode_token(credentials.credentials, expected=TokenType.ACCESS)
    return Actor(id=claims.sub)
