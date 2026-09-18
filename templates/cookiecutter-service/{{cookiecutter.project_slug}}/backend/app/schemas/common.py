"""Shared wire contracts.

The error body is a contract like any other: one shape for every failure, so a generated client
has one thing to handle instead of per-endpoint improvisation.
See docs/rules/backend/fastapi-standards.md.
"""

from __future__ import annotations

from pydantic import BaseModel, ConfigDict, Field


class ErrorDetail(BaseModel):
    model_config = ConfigDict(from_attributes=True)

    code: str = Field(description="Stable, machine-readable error code.", examples=["not_found"])
    message: str = Field(description="Human-readable explanation. Safe to show a developer.")
    #: Present on unhandled failures. A support ticket carrying this is one lookup from the event.
    trace_id: str | None = None


class ErrorResponse(BaseModel):
    error: ErrorDetail
