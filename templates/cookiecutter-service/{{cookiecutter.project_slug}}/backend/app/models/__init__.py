{% if cookiecutter.use_postgres == "yes" -%}
"""Public surface for Alembic autogenerate.

`alembic/env.py` imports `Base` from here, and autogenerate only sees a table if its module has
been imported. A model missing from this file produces an empty migration and a confusing hour, so
every new module under `app/models/` is imported here in the same change.
"""

from app.models.base import Base, TimestampMixin, UUIDMixin
from app.models.user import User

__all__ = ["Base", "TimestampMixin", "UUIDMixin", "User"]
{%- endif %}
