"""Payload factories.

One place per payload, so renaming a field is one edit instead of forty. The email is unique per
call so the tests survive being run without rollback isolation, and the password is a passphrase
rather than `hunter2` because the schema enforces a twelve-character floor.
"""

from __future__ import annotations

import uuid
from typing import Any

PASSWORD = "correct horse battery staple"
NEW_PASSWORD = "a different passphrase entirely"


def register_payload(**overrides: Any) -> dict[str, Any]:
    return {
        "email": f"ada-{uuid.uuid4().hex[:8]}@example.com",
        "password": PASSWORD,
        **overrides,
    }
