"""Health contracts."""

from __future__ import annotations

from pydantic import BaseModel, Field


class ReadinessStatus(BaseModel):
    status: str = Field(examples=["ok", "degraded"])
    #: The build that is answering. Confirming which revision is live is then one curl.
    version: str
    {%- if cookiecutter.use_postgres == "yes" %}
    database: str = Field(description="`ok` when the database answered a trivial query.")
    {%- endif %}
