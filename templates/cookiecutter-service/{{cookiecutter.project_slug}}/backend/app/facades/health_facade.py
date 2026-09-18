"""Readiness as a use case: ask the services, shape the answer for the wire."""

from __future__ import annotations

from app.schemas.health import ReadinessStatus
from app.services.health_service import HealthService


class HealthFacade:
    def __init__(self, health: HealthService, *, version: str) -> None:
        self._health = health
        self._version = version

    async def readiness(self) -> ReadinessStatus:
        {%- if cookiecutter.use_postgres == "yes" %}
        database_ok = await self._health.database_reachable()
        return ReadinessStatus(
            status="ok" if database_ok else "degraded",
            version=self._version,
            database="ok" if database_ok else "unreachable",
        )
        {%- else %}
        ready = await self._health.dependencies_reachable()
        return ReadinessStatus(status="ok" if ready else "degraded", version=self._version)
        {%- endif %}
