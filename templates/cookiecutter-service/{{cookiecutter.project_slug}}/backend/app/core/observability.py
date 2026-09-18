"""Sentry wiring.

Optional by construction: an empty DSN means no error tracking, and that MUST be the local default.
Developer laptops reporting into a real project make `environment` filtering useless.
See docs/rules/observability/error-tracking-with-sentry.md.
"""

from __future__ import annotations

from typing import Any

import sentry_sdk
from sentry_sdk.integrations.asyncio import AsyncioIntegration
from sentry_sdk.integrations.fastapi import FastApiIntegration
{%- if cookiecutter.use_postgres == "yes" %}
from sentry_sdk.integrations.sqlalchemy import SqlalchemyIntegration
{%- endif %}
from sentry_sdk.types import Event, Hint

from app.core.config import Settings

#: Keys whose values never leave the process. Sentry retains payloads, so a leaked token here is a
#: rotation exercise, not a log line.
DENY_KEYS = frozenset(
    {"authorization", "cookie", "password", "token", "api_key", "secret", "id_token"}
)


def scrub_event(event: Event, _hint: Hint) -> Event | None:
    request: dict[str, Any] = event.get("request") or {}
    for section_name in ("headers", "cookies", "data"):
        section = request.get(section_name)
        if isinstance(section, dict):
            for key in list(section):
                if key.lower() in DENY_KEYS:
                    section[key] = "[Filtered]"
    return event


def init_sentry(settings: Settings) -> None:
    if not settings.sentry_dsn:
        return
    sentry_sdk.init(
        dsn=settings.sentry_dsn,
        environment=settings.environment,
        # Without `release`, nothing can be attributed to a deploy and suspect commits never work.
        release=settings.git_sha,
        send_default_pii=False,
        traces_sample_rate=settings.sentry_traces_sample_rate,
        integrations=[
            FastApiIntegration(),
            AsyncioIntegration(),
            {%- if cookiecutter.use_postgres == "yes" %}
            SqlalchemyIntegration(),
            {%- endif %}
        ],
        before_send=scrub_event,
    )
