"""Payload factories, and the one type the fixtures share.

One place per payload, so renaming a field is one edit instead of forty. The email is unique per
call so the tests survive being run without rollback isolation, and the password is a passphrase
rather than `hunter2` because the schema enforces a twelve-character floor.
"""

from __future__ import annotations

import uuid
from contextlib import AbstractAsyncContextManager
from typing import Any, Protocol

from httpx import AsyncClient

from app.core.config import Settings


class ClientFactory(Protocol):
    """What the `client_for` fixture returns: settings in, an HTTP client bound to the test's
    transaction out, for a test that needs the app built differently from the default.

    `raise_app_exceptions=False` makes the client behave like a real server's caller: an unhandled
    error arrives as the 500 the catch-all handler wrote instead of being raised into the test.
    """

    def __call__(
        self, settings: Settings, /, *, raise_app_exceptions: bool = True
    ) -> AbstractAsyncContextManager[AsyncClient]: ...


PASSWORD = "correct horse battery staple"
NEW_PASSWORD = "a different passphrase entirely"


def register_payload(**overrides: Any) -> dict[str, Any]:
    return {
        "email": f"ada-{uuid.uuid4().hex[:8]}@example.com",
        "password": PASSWORD,
        **overrides,
    }
