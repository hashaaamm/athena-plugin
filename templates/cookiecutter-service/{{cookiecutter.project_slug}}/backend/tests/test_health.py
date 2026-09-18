"""Health endpoints. One test each — enough to prove the service accepts traffic at all."""

from __future__ import annotations

from httpx import AsyncClient


async def test_live_touches_nothing(client: AsyncClient) -> None:
    response = await client.get("/health/live")

    assert response.status_code == 200
    assert response.json() == {"status": "ok"}


async def test_ready_reports_dependencies(client: AsyncClient) -> None:
    response = await client.get("/health/ready")

    assert response.status_code == 200
    body = response.json()
    assert body["status"] == "ok"
    {%- if cookiecutter.use_postgres == "yes" %}
    assert body["database"] == "ok"
    {%- endif %}
