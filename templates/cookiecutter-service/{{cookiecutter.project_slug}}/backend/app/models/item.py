"""The example resource. Rename it, or delete it once you have a real one.

Copying this file end to end and renaming is the intended way to add a resource. Ask Athena
for the project structure rules.
"""

from __future__ import annotations

from sqlalchemy import String, Text
from sqlalchemy.orm import Mapped, mapped_column

from app.models.base import Base, TimestampMixin, UUIDMixin


class Item(Base, UUIDMixin, TimestampMixin):
    __tablename__ = "items"

    # Uniqueness is enforced here *and* checked in the service: the constraint is the guarantee,
    # the service check is what turns a race into a 409 instead of a 500.
    name: Mapped[str] = mapped_column(String(200), nullable=False, unique=True, index=True)
    description: Mapped[str | None] = mapped_column(Text, nullable=True)
