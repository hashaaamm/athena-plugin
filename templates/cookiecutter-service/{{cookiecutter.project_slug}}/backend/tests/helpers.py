"""Payload factories, and the one type the fixtures share.

One place per payload, so renaming a field is one edit instead of forty. The email is unique per
call so the tests survive being run without rollback isolation, and the password is a passphrase
rather than `hunter2` because the schema enforces a twelve-character floor.
"""

from __future__ import annotations

import uuid
from collections.abc import Callable
from contextlib import AbstractAsyncContextManager
from typing import Any

from httpx import AsyncClient

from app.core.config import Settings

#: What the `client_for` fixture returns: settings in, an HTTP client bound to the test's
#: transaction out, for a test that needs the app built differently from the default.
ClientFactory = Callable[[Settings], AbstractAsyncContextManager[AsyncClient]]

PASSWORD = "correct horse battery staple"
NEW_PASSWORD = "a different passphrase entirely"


def register_payload(**overrides: Any) -> dict[str, Any]:
    return {
        "email": f"ada-{uuid.uuid4().hex[:8]}@example.com",
        "password": PASSWORD,
        **overrides,
    }
