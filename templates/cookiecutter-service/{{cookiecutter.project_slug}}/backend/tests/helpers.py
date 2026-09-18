"""Payload factories.

One place per payload, so renaming a field is one edit instead of forty. Values are unique per call
so the tests survive being run without rollback isolation.
"""

from __future__ import annotations

import uuid
from typing import Any


def item_create_payload(**overrides: Any) -> dict[str, Any]:
    return {
        "name": f"item-{uuid.uuid4().hex[:8]}",
        "description": "Created by a test.",
        **overrides,
    }
