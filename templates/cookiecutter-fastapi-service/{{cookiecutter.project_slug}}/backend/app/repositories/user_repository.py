"""All User data access.

A method name containing "and", or containing a business term, belongs in the service instead.
Nothing here decides whether a password was correct; this layer only knows rows.
"""

from __future__ import annotations

from sqlalchemy import select

from app.models.user import User
from app.repositories.base import BaseRepository


class UserRepository(BaseRepository[User]):
    model = User

    async def get_by_email(self, email: str) -> User | None:
        """The email is assumed already normalised — see `app/schemas/auth.py`."""
        user: User | None = await self.session.scalar(select(User).where(User.email == email))
        return user

    async def create(self, *, email: str, hashed_password: str) -> User:
        return await self.add(User(email=email, hashed_password=hashed_password))

    async def set_password_hash(self, user: User, hashed_password: str) -> User:
        """Replace the stored digest. `flush()`, never `commit()` — the session dependency owns
        the transaction, so a failure later in the use case undoes this too."""
        user.hashed_password = hashed_password
        await self.session.flush()
        return user
