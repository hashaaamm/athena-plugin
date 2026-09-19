"""Application factory and lifespan.

A factory rather than a module-level `FastAPI()` with side effects, because a test needs to build
an app with overrides. Ask Athena for the FastAPI standards.
"""

from __future__ import annotations

from collections.abc import AsyncIterator
from contextlib import asynccontextmanager

import structlog
from fastapi import FastAPI
{%- if cookiecutter.include_frontend == "yes" %}
from fastapi.middleware.cors import CORSMiddleware
{%- endif %}

from app.api.router import api_router
from app.api.v1 import health
from app.core.config import Settings, get_settings
{%- if cookiecutter.use_postgres == "yes" %}
from app.core.database import dispose_engine
{%- endif %}
from app.core.exceptions import register_exception_handlers
from app.core.logging import configure_logging
{%- if cookiecutter.use_sentry == "yes" %}
from app.core.observability import init_sentry
{%- endif %}

logger = structlog.get_logger(__name__)


def _lifespan(settings: Settings):  # type: ignore[no-untyped-def]
    @asynccontextmanager
    async def lifespan(_: FastAPI) -> AsyncIterator[None]:
        configure_logging(settings)
        {%- if cookiecutter.use_sentry == "yes" %}
        init_sentry(settings)
        {%- endif %}
        logger.info("startup", environment=settings.environment, version=settings.git_sha)
        yield
        {%- if cookiecutter.use_postgres == "yes" %}
        # Release connections on shutdown, or every redeploy leaks them until Postgres refuses
        # new ones. Cloud Run sends SIGTERM and waits, so this reliably runs.
        await dispose_engine()
        {%- endif %}
        logger.info("shutdown")

    return lifespan


def create_app(settings: Settings | None = None) -> FastAPI:
    settings = settings or get_settings()

    app = FastAPI(
        title="{{ cookiecutter.project_name }}",
        description="{{ cookiecutter.description }}",
        version="0.1.0",
        lifespan=_lifespan(settings),
        # Interactive docs are a local and staging affordance. In production they are free
        # reconnaissance unless the API is deliberately public.
        docs_url=None if settings.environment == "production" else "/docs",
        redoc_url=None,
        openapi_url=None if settings.environment == "production" else "/openapi.json",
    )

    register_exception_handlers(app)
{%- if cookiecutter.include_frontend == "yes" %}

    # The SPA is served from a different origin than the API — a different port locally, a
    # different host deployed — so without this every request from the browser fails preflight
    # and the network tab is the only place that says why. Configured, never wildcarded: an
    # empty CORS_ORIGINS means no browser may call this API, which is the safe default for a
    # deployment that has no web client.
    if settings.cors_origin_list:
        app.add_middleware(
            CORSMiddleware,
            allow_origins=settings.cors_origin_list,
            allow_credentials=True,
            allow_methods=["*"],
            allow_headers=["*"],
        )
{%- endif %}
    app.include_router(health.router)
    app.include_router(api_router, prefix=settings.api_v1_prefix)

    return app


app = create_app()
