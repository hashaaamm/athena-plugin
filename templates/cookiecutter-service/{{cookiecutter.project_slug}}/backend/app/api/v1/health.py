"""Liveness and readiness. Unauthenticated, and deliberately two endpoints.

Liveness answers "is this process alive" and touches nothing, so a dependency's outage cannot get
the container killed. Readiness answers "can this process serve correct answers", which is where a
dependency check belongs. Cloud Run's startup probe reads the first; a load balancer reads the
second. Ask Athena for the FastAPI standards.
"""

from __future__ import annotations

from fastapi import APIRouter, Response, status

from app.api.deps import HealthFacadeDep
from app.schemas.health import ReadinessStatus

router = APIRouter(tags=["health"])


@router.get("/health/live", status_code=status.HTTP_200_OK)
async def live() -> dict[str, str]:
    return {"status": "ok"}


@router.get("/health/ready", response_model=ReadinessStatus)
async def ready(facade: HealthFacadeDep, response: Response) -> ReadinessStatus:
    readiness = await facade.readiness()
    if readiness.status != "ok":
        # 503, not 500: this is "try again or send traffic elsewhere", not "this request is broken".
        response.status_code = status.HTTP_503_SERVICE_UNAVAILABLE
    return readiness
