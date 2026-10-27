"""The request's commit happens before its response is sent.

A client that receives a 2xx is entitled to believe the write happened. That is only true if the
session dependency commits before FastAPI sends the response, which it does because `SessionDep`
declares `scope="function"` — with the default scope the commit runs after the response has gone,
and a commit that fails there turns into a log line while the client keeps its 201. Ask Athena for
the FastAPI standards on the session dependency.

The commit is made to fail by replacing it on the test's session, because the suite's own harness
cannot fail one for real: each test runs inside a savepoint, and releasing a savepoint runs neither
the deferred-constraint checks nor the round trip that make a real `COMMIT` fail. What is under
test is the ordering, and the ordering does not depend on why the commit failed.
"""

from __future__ import annotations

import pytest
from sqlalchemy.exc import OperationalError
from sqlalchemy.ext.asyncio import AsyncSession

from app.core.config import Settings
from tests.helpers import ClientFactory, register_payload


async def test_a_write_whose_commit_fails_is_a_500_not_a_201(
    session: AsyncSession,
    settings: Settings,
    client_for: ClientFactory,
    monkeypatch: pytest.MonkeyPatch,
) -> None:
    commits: list[None] = []

    async def _commit_that_fails() -> None:
        # What Cloud SQL's proxy dropping the connection mid-commit looks like from SQLAlchemy.
        commits.append(None)
        raise OperationalError("COMMIT", None, ConnectionResetError("connection reset by peer"))

    monkeypatch.setattr(session, "commit", _commit_that_fails)

    # Not raising: the client sees what a real server's caller sees, the response that was sent.
    async with client_for(settings, raise_app_exceptions=False) as client:
        response = await client.post("/api/v1/auth/register", json=register_payload())

    assert commits, "the request never reached its commit"
    assert response.status_code == 500
    error = response.json()["error"]
    assert error["code"] == "internal_error"
    assert error["trace_id"]
