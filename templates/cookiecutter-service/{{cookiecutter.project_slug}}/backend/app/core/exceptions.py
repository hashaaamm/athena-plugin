"""The application error hierarchy, and the handlers that turn it into responses.

Layers below the transport raise these. Nothing below `app/api` knows what an HTTP status code is —
a service must be callable from a worker, a CLI or a test with no HTTP stack anywhere.
Ask Athena for the FastAPI standards and for how errors and logging are handled.
"""

from __future__ import annotations

import uuid
from typing import ClassVar

{% if cookiecutter.use_sentry == "yes" -%}
import sentry_sdk
{% endif -%}
import structlog
from fastapi import FastAPI, Request, status
from fastapi.responses import JSONResponse

from app.schemas.common import ErrorDetail, ErrorResponse

logger = structlog.get_logger(__name__)


class AppError(Exception):
    """Base class for every error this application raises deliberately.

    The status code lives with the error class, not at the raise site, so one rule cannot return
    404 from one caller and 400 from another.
    """

    status_code: int = status.HTTP_400_BAD_REQUEST
    code: str = "bad_request"
    default_message: str = "Application error"
    #: Response headers this error requires. Exactly one error needs them today and it is a
    #: protocol requirement, not a nicety — see `UnauthorizedError`.
    headers: ClassVar[dict[str, str] | None] = None

    def __init__(self, message: str | None = None) -> None:
        self.message = message or self.default_message
        super().__init__(self.message)


class NotFoundError(AppError):
    status_code = status.HTTP_404_NOT_FOUND
    code = "not_found"
    default_message = "Resource not found"


class ConflictError(AppError):
    status_code = status.HTTP_409_CONFLICT
    code = "conflict"
    default_message = "Resource conflict"


class ValidationError(AppError):
    """A rule the schema cannot express: uniqueness, ownership, a state machine."""

    # The literal, not the constant: Starlette renamed it, and importing either name pins this
    # file to a version range for no benefit.
    status_code = 422
    code = "validation_error"
    default_message = "Request failed validation"


class UnauthorizedError(AppError):
    status_code = status.HTTP_401_UNAUTHORIZED
    code = "unauthorized"
    default_message = "Not authenticated"
    # A 401 without `WWW-Authenticate` is a protocol violation, and the practical symptom is an
    # HTTP client that will not attempt re-authentication because nothing told it which scheme to
    # use. RFC 9110 requires the header on every 401.
    headers: ClassVar[dict[str, str] | None] = {"WWW-Authenticate": "Bearer"}


class ForbiddenError(AppError):
    status_code = status.HTTP_403_FORBIDDEN
    code = "forbidden"
    default_message = "Not permitted"


def _body(code: str, message: str, trace_id: str | None = None) -> dict[str, object]:
    return ErrorResponse(
        error=ErrorDetail(code=code, message=message, trace_id=trace_id)
    ).model_dump()


def register_exception_handlers(app: FastAPI) -> None:
    @app.exception_handler(AppError)
    async def _handle_app_error(_request: Request, exc: AppError) -> JSONResponse:
        # Deliberate errors are control flow, not defects: logged at warning, never reported.
        logger.warning("app_error", code=exc.code, message=exc.message)
        return JSONResponse(
            status_code=exc.status_code,
            content=_body(exc.code, exc.message),
            headers=exc.headers,
        )

    @app.exception_handler(Exception)
    async def _handle_unexpected(_request: Request, exc: Exception) -> JSONResponse:
        """Report once, with full context, and hand the client the lookup key.

        Catching and logging `str(exc)` instead would lose the stack, the locals and the trace —
        which is the whole reason the error tracker exists.
        """
        trace_id = str(uuid.uuid4())
        {%- if cookiecutter.use_sentry == "yes" %}
        with sentry_sdk.new_scope() as scope:
            scope.set_tag("trace_id", trace_id)
            sentry_sdk.capture_exception(exc)
        {%- endif %}
        logger.exception("unhandled_error", trace_id=trace_id, error=type(exc).__name__)
        return JSONResponse(
            status_code=status.HTTP_500_INTERNAL_SERVER_ERROR,
            content=_body("internal_error", "Internal server error", trace_id),
        )
