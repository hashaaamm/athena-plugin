"""What "ready" means for this service.

Liveness and readiness answer different questions, which is why they are different endpoints.
Liveness touches nothing: a probe that checks the database will restart the application during a
database blip, turning a dependency's bad minute into an outage of your own.
"""

from __future__ import annotations
{% if cookiecutter.use_postgres == "yes" %}
from app.repositories.health_repository import HealthRepository


class HealthService:
    def __init__(self, health: HealthRepository) -> None:
        self._health = health

    async def database_reachable(self) -> bool:
        try:
            return await self._health.ping()
        except Exception:
            # Any failure to reach the database means "not ready" — including the ones we have not
            # thought of. Readiness is the one place a bare except is the correct answer.
            return False
{%- else %}

class HealthService:
    """No dependencies yet, so readiness is liveness. Add checks here as they appear."""

    async def dependencies_reachable(self) -> bool:
        return True
{%- endif %}
