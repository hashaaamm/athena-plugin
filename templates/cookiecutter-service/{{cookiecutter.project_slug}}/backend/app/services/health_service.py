"""What "ready" means for this service, and the answer the probe gets.

Liveness and readiness answer different questions, which is why they are different endpoints.
Liveness touches nothing: a probe that checks the database will restart the application during a
database blip, turning a dependency's bad minute into an outage of your own.

`readiness()` is the use case, so it returns the response schema. The checks it is built from stay
separate methods, because a second dependency is another check rather than another shape.
"""

from __future__ import annotations
{% if cookiecutter.use_postgres == "yes" %}
from app.repositories.health_repository import HealthRepository
from app.schemas.health import ReadinessStatus


class HealthService:
    def __init__(self, health: HealthRepository, *, version: str) -> None:
        self._health = health
        self._version = version

    async def readiness(self) -> ReadinessStatus:
        database_ok = await self.database_reachable()
        return ReadinessStatus(
            status="ok" if database_ok else "degraded",
            version=self._version,
            database="ok" if database_ok else "unreachable",
        )

    async def database_reachable(self) -> bool:
        try:
            return await self._health.ping()
        except Exception:
            # Any failure to reach the database means "not ready" — including the ones we have not
            # thought of. Readiness is the one place a bare except is the correct answer.
            return False
{%- else %}
from app.schemas.health import ReadinessStatus


class HealthService:
    """No dependencies yet, so readiness is liveness. Add checks here as they appear."""

    def __init__(self, *, version: str) -> None:
        self._version = version

    async def readiness(self) -> ReadinessStatus:
        ready = await self.dependencies_reachable()
        return ReadinessStatus(status="ok" if ready else "degraded", version=self._version)

    async def dependencies_reachable(self) -> bool:
        return True
{%- endif %}
