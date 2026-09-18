{% if cookiecutter.use_postgres == "yes" -%}
"""Public surface for Alembic autogenerate.

`alembic/env.py` imports `Base` from here, and autogenerate only sees a table if its module has
been imported. A model missing from this file produces an empty migration and a confusing hour.
"""

from app.models.base import Base, TimestampMixin, UUIDMixin
from app.models.item import Item

__all__ = ["Base", "Item", "TimestampMixin", "UUIDMixin"]
{%- endif %}
