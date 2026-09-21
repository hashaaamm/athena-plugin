"""The HTTP contract, through the full stack: route -> service -> repository -> Postgres.

Nothing is mocked. These tests are what prove the layers are actually wired together, which no
amount of unit testing at the service layer can tell you.
"""

from __future__ import annotations

import uuid

import pytest
from httpx import AsyncClient

from tests.helpers import item_create_payload


async def test_create_and_read_back(client: AsyncClient) -> None:
    payload = item_create_payload(name="widget")

    created = await client.post("/api/v1/items", json=payload)
    assert created.status_code == 201
    body = created.json()
    assert body["name"] == "widget"

    fetched = await client.get(f"/api/v1/items/{body['id']}")
    assert fetched.status_code == 200
    assert fetched.json() == body


async def test_duplicate_name_returns_409_in_the_documented_shape(client: AsyncClient) -> None:
    payload = item_create_payload(name="widget")
    await client.post("/api/v1/items", json=payload)

    response = await client.post("/api/v1/items", json=payload)

    assert response.status_code == 409
    # One error contract for the whole API — see app/schemas/common.py.
    assert response.json()["error"]["code"] == "conflict"


async def test_unknown_id_returns_404(client: AsyncClient) -> None:
    response = await client.get(f"/api/v1/items/{uuid.uuid4()}")

    assert response.status_code == 404
    assert response.json()["error"]["code"] == "not_found"


@pytest.mark.parametrize(
    ("payload", "expected_status"),
    [
        ({"name": ""}, 422),
        ({"name": "x" * 201}, 422),
        ({"description": "no name at all"}, 422),
        ({"name": "valid"}, 201),
    ],
    ids=["empty-name", "name-too-long", "missing-name", "valid"],
)
async def test_validation(
    client: AsyncClient, payload: dict[str, object], expected_status: int
) -> None:
    response = await client.post("/api/v1/items", json=payload)

    assert response.status_code == expected_status


async def test_list_is_paginated(client: AsyncClient) -> None:
    for _ in range(3):
        await client.post("/api/v1/items", json=item_create_payload())

    response = await client.get("/api/v1/items", params={"limit": 2})

    assert response.status_code == 200
    assert response.json()["count"] == 2


async def test_delete_returns_204(client: AsyncClient) -> None:
    created = await client.post("/api/v1/items", json=item_create_payload())

    response = await client.delete(f"/api/v1/items/{created.json()['id']}")

    assert response.status_code == 204
