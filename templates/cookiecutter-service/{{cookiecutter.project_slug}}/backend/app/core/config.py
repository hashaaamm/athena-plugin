"""Application settings.

This module is the only place in the process that reads the environment.
The FastAPI standards MUST it: `os.environ` does not appear anywhere else in
application code, because a stray read is invisible to config validation.
"""

from __future__ import annotations

from functools import lru_cache
from typing import Literal{% if cookiecutter.use_postgres == "yes" %}, Self{% endif %}

{% if cookiecutter.use_postgres == "yes" -%}
from pydantic import SecretStr, computed_field, model_validator
{%- else -%}
from pydantic import computed_field
{%- endif %}
from pydantic_settings import BaseSettings, SettingsConfigDict

Environment = Literal["local", "test", "staging", "production"]
{% if cookiecutter.use_postgres == "yes" %}

# A sentinel, not a credential. The validator below refuses to start with it outside local and
# test — a placeholder password in production is worse than a missing one, because it starts.
PLACEHOLDER_SECRET = "local-only-not-a-secret"  # noqa: S105
{% endif %}

class Settings(BaseSettings):
    """Typed configuration, loaded from the environment and an optional local `.env`."""

    model_config = SettingsConfigDict(
        env_file=".env",
        env_file_encoding="utf-8",
        extra="ignore",
    )

    # --- Application -----------------------------------------------------
    environment: Environment = "local"
    app_name: str = "{{ cookiecutter.project_slug }}"
    api_v1_prefix: str = "/api/v1"
    log_level: str = "INFO"
    json_logs: bool = True
    #: The immutable build SHA. CI sets it; Sentry needs it to attribute a regression to a commit.
    git_sha: str = "local"
{% if cookiecutter.include_frontend == "yes" %}
    # --- Browser client --------------------------------------------------
    #: Comma-separated origins the SPA is served from, e.g. "http://localhost:3000".
    #: Empty — the default — means no cross-origin access at all, which is correct for a service
    #: with no browser client. There is deliberately no "allow everything" setting: a wildcard
    #: origin cannot carry credentials anyway, so it buys nothing but a finding in a pen test.
    cors_origins: str = ""
{% endif %}{% if cookiecutter.use_postgres == "yes" %}
    # --- Database --------------------------------------------------------
    postgres_host: str = "db"
    postgres_port: int = 5432
    postgres_user: str = "{{ cookiecutter.project_slug.replace('-', '_') }}"
    postgres_password: SecretStr = SecretStr(PLACEHOLDER_SECRET)
    postgres_db: str = "{{ cookiecutter.project_slug.replace('-', '_') }}"
    #: Cloud SQL's Unix socket, or a full DSN in tests. Set this and the parts above are ignored.
    database_url_override: str = ""
{% endif %}{% if cookiecutter.use_sentry == "yes" %}
    # --- Observability ---------------------------------------------------
    #: Empty means no error tracking. That MUST be the local default —
    #: ask Athena for the Sentry error-tracking rules.
    sentry_dsn: str = ""
    sentry_traces_sample_rate: float = 0.1
{% endif %}
    @computed_field  # type: ignore[prop-decorator]
    @property
    def is_deployed(self) -> bool:
        return self.environment in ("staging", "production")
{% if cookiecutter.include_frontend == "yes" %}
    @computed_field  # type: ignore[prop-decorator]
    @property
    def cors_origin_list(self) -> list[str]:
        """Parsed once, here, so `.env` can hold a plain comma-separated string and no caller
        has to remember to strip whitespace."""
        return [origin.strip() for origin in self.cors_origins.split(",") if origin.strip()]
{% endif %}{% if cookiecutter.use_postgres == "yes" %}
    @computed_field  # type: ignore[prop-decorator]
    @property
    def database_url(self) -> str:
        """The async SQLAlchemy DSN."""
        if self.database_url_override:
            return self.database_url_override
        password = self.postgres_password.get_secret_value()
        return (
            f"postgresql+asyncpg://{self.postgres_user}:{password}"
            f"@{self.postgres_host}:{self.postgres_port}/{self.postgres_db}"
        )
{% endif %}{% if cookiecutter.use_postgres == "yes" %}
    @model_validator(mode="after")
    def _reject_placeholder_credentials_when_deployed(self) -> Self:
        """Fail at startup rather than at first use.

        Only credentials *every* process needs belong here. A credential that one entry point
        legitimately lacks — a migration job with no outbound API key, say — is checked where it is
        used, or least privilege turns into a start-up crash.
        """
        if not self.is_deployed:
            return self
        placeholder = self.postgres_password.get_secret_value() == PLACEHOLDER_SECRET
        if placeholder and not self.database_url_override:
            raise ValueError("POSTGRES_PASSWORD must be set outside local and test")
        return self
{% endif %}

@lru_cache
def get_settings() -> Settings:
    """Cached accessor.

    A test that changes the environment MUST call `get_settings.cache_clear()`, or it will debug a
    cached config for twenty minutes.
    """
    return Settings()
