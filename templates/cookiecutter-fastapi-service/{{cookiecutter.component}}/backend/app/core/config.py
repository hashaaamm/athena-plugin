"""Application settings.

This module is the only place in the process that reads the environment.
The FastAPI standards MUST it: `os.environ` does not appear anywhere else in
application code, because a stray read is invisible to config validation.
"""

from __future__ import annotations

{% if cookiecutter.use_postgres == "yes" -%}
import re
{% endif -%}
from functools import lru_cache
from typing import Literal, Self
from urllib.parse import urlsplit

{% if cookiecutter.use_postgres == "yes" -%}
from pydantic import SecretStr, computed_field, model_validator
{%- else -%}
from pydantic import computed_field, model_validator
{%- endif %}
from pydantic_settings import BaseSettings, SettingsConfigDict
{%- if cookiecutter.use_postgres == "yes" %}
from sqlalchemy.engine import URL
{%- endif %}

Environment = Literal["local", "test", "staging", "production"]
{%- if cookiecutter.use_postgres == "yes" %}

#: Where the refresh token travels. A property of the client, not of the session: a
#: server-to-server caller keeps it in the JSON body, a browser must keep it where script cannot
#: read it — an httpOnly cookie. `app/api/router.py` mounts one set of routes or the other.
RefreshTransport = Literal["body", "cookie"]

#: How the process proves who it is to Postgres. `password` is local development, tests, CI and the
#: migration {% if cookiecutter.deploy_target == "aws-ecs-express" %}task{% else %}job{% endif %}, which connects as the database's admin login. {% if cookiecutter.deploy_target == "aws-ecs-express" %}`rds-iam` is the deployed
#: service: a token signed with the task role's own credentials, minted for every new connection,
#: and no password anywhere.{% else %}`cloud-sql-iam` is the
#: deployed service: the Cloud SQL connector authenticates as the revision's service account, and
#: there is no password anywhere.{% endif %} The same names exist in every service template built to this
#: handbook; the other IAM mode, {% if cookiecutter.deploy_target == "aws-ecs-express" %}`cloud-sql-iam`{% else %}`rds-iam`{% endif %}, belongs to the other deploy target and is not
#: rendered here, so its driver is not a dependency of this one.
DatabaseAuth = Literal["password", "{% if cookiecutter.deploy_target == "aws-ecs-express" %}rds-iam{% else %}cloud-sql-iam{% endif %}"]

#: How the deployed service's own database role logs in, which decides what the migration does to
#: it: `rds-iam` creates the role if it is missing and grants it `rds_iam`; `cloud-sql-iam` expects
#: Cloud SQL to have created it, as the IAM user the stack declares. Both are plain SQL, so both
#: are accepted whichever target this was generated for.
AppAuth = Literal["rds-iam", "cloud-sql-iam"]

#: A role name the migration grants privileges to, embedded in the grant script as a quoted
#: identifier. Letters, digits and `_ . @ -` cover a plain role and a Cloud SQL IAM user
#: (`name@project.iam`); a quote or a `$` would not survive the trip, so they are refused here.
APP_ROLE_PATTERN = re.compile(r"[A-Za-z0-9_.@-]{1,63}")

#: `DATABASE_PORT` when it is unset.
DEFAULT_DATABASE_PORT = 5432
{%- endif %}
{% if cookiecutter.use_postgres == "yes" %}

# A sentinel, not a credential. The validator below refuses to start with it outside local and
# test — a placeholder password in production is worse than a missing one, because it starts.
PLACEHOLDER_SECRET = "local-only-not-a-secret"  # noqa: S105

#: HS256 signs with the configured secret directly, so the secret *is* the key. RFC 7518 §3.2 puts
#: the floor at the hash's own output size — 32 bytes for SHA-256 — and PyJWT warns below it; a
#: shorter key is brute-forceable offline from one captured token, and captured tokens are the
#: normal case. Deployed, `just infra-secrets` generates it — `openssl rand -base64 48`, URL-safe —
#: and adds it to the secret store without ever printing it.
MIN_JWT_SECRET_LENGTH = 32

#: The signing equivalent of PLACEHOLDER_SECRET, and long enough to clear that floor on purpose:
#: a shorter sentinel would make PyJWT warn on every token a developer mints, which under this
#: project's `filterwarnings = ["error"]` is a red test suite on a fresh clone. It is still a
#: sentinel and `require_signing_key` still refuses to serve with it anywhere deployed.
PLACEHOLDER_JWT_SECRET = "local-only-not-a-real-signing-key"  # noqa: S105
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

    # --- Browser client --------------------------------------------------
    #: `CORS_ORIGINS`: comma-separated origins a browser client is served from, e.g.
    #: "http://localhost:3000". Empty — the default — means no cross-origin access at all, which is
    #: right for a service with no browser client. There is deliberately no "allow everything": a
    #: wildcard origin cannot carry credentials anyway, so it buys nothing but a finding in a pen
    #: test. Read it as `cors_origin_list`.
    cors_origins: str = ""
{% if cookiecutter.use_postgres == "yes" %}
    # --- Database --------------------------------------------------------
    #: `DATABASE_AUTH`: see `DatabaseAuth`. `password` unless something deployed says otherwise.
    database_auth: DatabaseAuth = "password"
    #: The local parts, `password` mode only. Compose hands the same `POSTGRES_*` values to the db
    #: container, which is why they keep the names the Postgres image reads.
    postgres_host: str = "db"
    postgres_port: int = 5432
    postgres_user: str = "{{ cookiecutter.project_slug.replace('-', '_') }}"
    postgres_password: SecretStr = SecretStr(PLACEHOLDER_SECRET)
    postgres_db: str = "{{ cookiecutter.project_slug.replace('-', '_') }}"
    #: The deployed parts. In `password` mode, setting `DATABASE_HOST` switches from the
    #: `POSTGRES_*` parts to these — all of them, never a mix — which is how the migration is
{%- if cookiecutter.deploy_target == "aws-ecs-express" %}
    #: handed the admin login out of RDS's own secret, one JSON key per variable.
{%- else %}
    #: handed the `postgres` login.
{%- endif %}
    #: In the IAM modes they are the connection itself, minus the password nobody has. A
    #: `DATABASE_HOST` that starts with `/` is a Unix socket directory: Cloud SQL's, at
    #: `/cloudsql/<connection-name>`.
    #:
    #: TLS for a DSN built from parts is the driver's: asyncpg reads `PGSSLMODE`, as libpq does.
    #: {% if cookiecutter.deploy_target == "aws-ecs-express" %}The migration task sets `PGSSLMODE=require`, because RDS refuses anything else; `rds-iam`
    #: always requires TLS.{% else %}The IAM mode does not need it: the connector carries its own TLS.{% endif %}
    database_host: str = ""
    #: `DEFAULT_DATABASE_PORT` when unset — see `database_tcp_port`.
    database_port: int | None = None
    database_name: str = ""
    database_user: str = ""
    database_password: SecretStr = SecretStr("")
    #: A full DSN in tests, or anywhere the parts are not enough. `password` mode only.
    database_url_override: str = ""
{%- if cookiecutter.deploy_target == "aws-ecs-express" %}
    #: `AWS_REGION`: the region an `rds-iam` token is signed for.
    aws_region: str = ""
{%- else %}
    #: `CLOUD_SQL_INSTANCE`: `project:region:instance`, which `cloud-sql-iam` connects to.
    cloud_sql_instance: str = ""
{%- endif %}
    #: `DATABASE_APP_ROLE` and `DATABASE_APP_AUTH`: the deployed service's own database role, and
    #: how it logs in, which the migration creates or checks and grants its privileges after every
    #: upgrade — see `alembic/env.py`. Both or neither; empty, the default, skips the step: locally
    #: the one Compose user owns everything already.
    database_app_role: str = ""
    database_app_auth: AppAuth | None = None

    # --- Authentication --------------------------------------------------
    #: The HS256 signing key for access tokens. There is no usable default: `require_signing_key`
    #: in `app/core/security/tokens.py` refuses the sentinel, and anything too short, everywhere
    #: except local and test. A template that shipped a working default here would ship every
    #: service generated from it the same forgeable key.
    jwt_secret: SecretStr = SecretStr(PLACEHOLDER_JWT_SECRET)
    #: `iss` and `aud`, verified on every decode. They are what stops a token minted by staging,
    #: or by another service that happens to share the key, from being accepted here.
    jwt_issuer: str = "{{ cookiecutter.project_slug }}"
    jwt_audience: str = "{{ cookiecutter.project_slug }}"
    #: Fifteen minutes. Nothing checks an access token against the database, so this number is
    #: also the revocation latency: revoking a session stops new access tokens at once, and the
    #: one already issued keeps working until it expires. Deactivating a user is the same sentence.
    access_token_ttl_seconds: int = 900
    #: How long a refresh token, and the `refresh_sessions` row behind it, stays usable. Every
    #: refresh replaces it with a new one, so this is the longest a client can go without calling
    #: the API before it has to sign in again — not how long a session can last.
    refresh_token_ttl_days: int = 30
    #: `body` or `cookie`: see `RefreshTransport`, and `app/api/refresh_cookie.py` for the cookie.
    refresh_transport: RefreshTransport = "body"
    #: The refresh cookie's `Secure` attribute. True everywhere it matters, and refused as false
    #: anywhere deployed: without it the browser sends the cookie over plain HTTP. Local development
    #: serves the API on `http://localhost`, which is why `.env.example` turns it off there.
    refresh_cookie_secure: bool = True
    #: The refresh cookie's `SameSite` attribute. `strict` keeps it off every request a third-party
    #: page starts. It also needs the SPA and the API on the same *site* — `app.example.com` and
    #: `api.example.com` are; two `*.run.app` hostnames are not, because `run.app` is a public
    #: suffix. `none` is the cross-site setting: the browser then attaches the cookie wherever it
    #: allows third-party cookies at all, and only the CSRF check in `app/api/refresh_cookie.py`
    #: stands between it and a forged request.
    refresh_cookie_samesite: Literal["strict", "lax", "none"] = "strict"
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

    @computed_field  # type: ignore[prop-decorator]
    @property
    def cors_origin_list(self) -> list[str]:
        """`CORS_ORIGINS` parsed, trimmed and with empty entries dropped, so `.env` can hold a plain
        comma-separated string and no caller has to remember to strip whitespace."""
        return [origin.strip() for origin in self.cors_origins.split(",") if origin.strip()]

    @model_validator(mode="after")
    def _refuse_an_entry_that_is_not_an_origin(self) -> Self:
        """Accept exactly what a browser sends in an `Origin` header: a scheme, a host and an
        optional port, nothing else.

        A trailing slash or a path never matches a real `Origin`, so such an entry would be dead
        configuration that looks alive; `*` is refused for the reason on `cors_origins`.
        """
        for origin in self.cors_origin_list:
            if not _is_origin(origin):
                raise ValueError(
                    f"CORS_ORIGINS entry {origin!r} must be an origin such as "
                    "https://app.example.com"
                )
        return self
{% if cookiecutter.use_postgres == "yes" %}
    @property
    def database_tcp_port(self) -> int:
        """`DATABASE_PORT`, or `DEFAULT_DATABASE_PORT` when it is unset."""
        return self.database_port or DEFAULT_DATABASE_PORT

    @computed_field  # type: ignore[prop-decorator]
    @property
    def database_url(self) -> str:
        """The async SQLAlchemy DSN. In `password` mode, first match wins:

        1. `DATABASE_URL_OVERRIDE`, verbatim;
        2. the `DATABASE_*` parts, when `DATABASE_HOST` is set;
        3. the `POSTGRES_*` parts — local development, the suite and CI.

        {% if cookiecutter.deploy_target == "aws-ecs-express" %}`rds-iam` is the `DATABASE_*` parts with no password and TLS required: the engine supplies a
        fresh token for every connection.{% else %}`cloud-sql-iam` names only the user and the database: the connector supplies the address,
        the TLS and the credential.{% endif %}
        """
        if self.database_auth == "password":
            if self.database_url_override:
                return self.database_url_override
            if not self.database_host:
                return _dsn(
                    self.postgres_host,
                    self.postgres_port,
                    self.postgres_db,
                    self.postgres_user,
                    self.postgres_password.get_secret_value(),
                )
            return _dsn(
                self.database_host,
                self.database_tcp_port,
                self.database_name,
                self.database_user,
                self.database_password.get_secret_value(),
            )
{%- if cookiecutter.deploy_target == "aws-ecs-express" %}
        # An IAM token is a bearer credential for fifteen minutes; it never crosses the wire in
        # the clear. RDS also refuses anything else (`rds.force_ssl`).
        return _dsn(
            self.database_host,
            self.database_tcp_port,
            self.database_name,
            self.database_user,
            None,
            ssl="require",
        )
{%- else %}
        return URL.create(
            "postgresql+asyncpg", username=self.database_user, database=self.database_name
        ).render_as_string(hide_password=False)
{%- endif %}

    @model_validator(mode="after")
    def _require_what_the_auth_mode_needs(self) -> Self:
        """Refuse a database configuration that cannot connect, at start-up rather than at the
        first query.

        Each mode reads its own settings, and each must be given: an IAM mode on an empty host is a
        deploy that cannot connect. A password or a URL beside IAM authentication is one nothing
        reads — and the sign of a stack wired to the wrong mode — so it is refused rather than
        ignored.
        """
        problems: list[str] = []

        def require(*pairs: tuple[str, str]) -> None:
            problems.extend(
                f"DATABASE_AUTH={self.database_auth} requires {env}"
                for env, value in pairs
                if not value.strip()
            )

        if self.database_auth == "password":
            if not self.database_url_override and self.database_host:
                require(
                    ("DATABASE_NAME", self.database_name), ("DATABASE_USER", self.database_user)
                )
        else:
{%- if cookiecutter.deploy_target == "aws-ecs-express" %}
            require(
                ("DATABASE_HOST", self.database_host),
                ("DATABASE_NAME", self.database_name),
                ("DATABASE_USER", self.database_user),
                ("AWS_REGION", self.aws_region),
            )
{%- else %}
            require(
                ("CLOUD_SQL_INSTANCE", self.cloud_sql_instance),
                ("DATABASE_NAME", self.database_name),
                ("DATABASE_USER", self.database_user),
            )
            # project:region:instance, where a domain-scoped project id carries a colon of its own.
            parts = self.cloud_sql_instance.split(":")
            if self.cloud_sql_instance and (len(parts) < 3 or "" in parts):
                problems.append(
                    f"CLOUD_SQL_INSTANCE {self.cloud_sql_instance!r} is not a connection name such "
                    "as project:region:instance"
                )
{%- endif %}
            if self.database_url_override or self.database_password.get_secret_value():
                problems.append(
                    f"DATABASE_AUTH={self.database_auth} takes no DATABASE_PASSWORD or "
                    "DATABASE_URL_OVERRIDE: the identity is the credential"
                )
        if self.database_port is not None and not 1 <= self.database_port <= 65535:
            problems.append(f"DATABASE_PORT must be between 1 and 65535, not {self.database_port}")
        # The bootstrap needs both or neither: a role with no way to log in is a role nothing uses,
        # and a way to log in with no role is a grant to nobody.
        if self.database_app_auth and not self.database_app_role:
            problems.append("DATABASE_APP_AUTH requires DATABASE_APP_ROLE")
        elif self.database_app_role and not self.database_app_auth:
            problems.append(
                "DATABASE_APP_ROLE requires DATABASE_APP_AUTH: rds-iam or cloud-sql-iam"
            )
        if self.database_app_role and not APP_ROLE_PATTERN.fullmatch(self.database_app_role):
            problems.append(
                f"DATABASE_APP_ROLE {self.database_app_role!r} must be 1-63 letters, digits "
                "or _ . @ -"
            )
        if problems:
            raise ValueError("; ".join(problems))
        return self
{% endif %}{% if cookiecutter.use_postgres == "yes" %}
    @model_validator(mode="after")
    def _reject_placeholder_credentials_when_deployed(self) -> Self:
        """Fail at startup rather than at first use.

        Only credentials *every* process needs belong here. A credential that one entry point
        legitimately lacks — a migration job with no outbound API key, say — is checked where it is
        used, or least privilege turns into a start-up crash.
        """
        if not self.is_deployed or self.database_auth != "password":
            return self
        password = (
            self.database_password if self.database_host else self.postgres_password
        ).get_secret_value()
        if not self.database_url_override and password in ("", PLACEHOLDER_SECRET):
            raise ValueError(
                "DATABASE_PASSWORD (POSTGRES_PASSWORD when DATABASE_HOST is unset) must be set "
                "outside local and test"
            )
        # `jwt_secret` is deliberately *not* checked here, and that is this docstring's rule
        # applied rather than an oversight: the migration job is granted no signing key because it
        # signs nothing, and a check in this validator would stop it booting. It is enforced in
        # `require_signing_key`, which `create_app` calls — so a deployed *server* still refuses to
        # start with the sentinel, which is the failure that matters.
        return self

    @model_validator(mode="after")
    def _refuse_a_cookie_transport_no_browser_can_use(self) -> Self:
        """Every browser sends `Origin` on a cross-origin POST, and on a same-origin one too, so the
        CSRF check in front of the cookie routes refuses every refresh unless the client's origin
        is listed. Starting anyway would be a sign-in that works and a session that never survives
        a reload, which is a long afternoon to diagnose."""
        if self.refresh_transport == "cookie" and not self.cors_origin_list:
            raise ValueError(
                "REFRESH_TRANSPORT=cookie requires CORS_ORIGINS: "
                "the CSRF check only accepts listed origins"
            )
        return self

    @model_validator(mode="after")
    def _refuse_an_unprotected_refresh_cookie(self) -> Self:
        """A refresh cookie without `Secure` is a thirty-day credential on plain HTTP.

        Browsers drop `SameSite=None` without `Secure` silently, so that pair is refused everywhere
        rather than shipped as a sign-in that mysteriously never persists.
        """
        if self.refresh_cookie_samesite == "none" and not self.refresh_cookie_secure:
            raise ValueError("REFRESH_COOKIE_SAMESITE=none requires REFRESH_COOKIE_SECURE=true")
        if self.is_deployed and not self.refresh_cookie_secure:
            raise ValueError("REFRESH_COOKIE_SECURE must be true outside local and test")
        return self
{% endif %}

{% if cookiecutter.use_postgres == "yes" -%}
def _dsn(
    host: str,
    port: int,
    database: str,
    user: str,
    password: str | None,
    **query: str,
) -> str:
    """An asyncpg DSN, built with SQLAlchemy's `URL` rather than an f-string, so a password
    holding a `@`, a `/` or a `%` is escaped instead of silently splitting the DSN somewhere else.

    A host that starts with `/` is a Unix socket directory — `/cloudsql/<connection-name>`, which
    Cloud Run mounts for a job — and travels as the `host` query parameter, because a path is not
    a URL authority. The port stays: it names the socket file, `.s.PGSQL.<port>`.
    """
    socket = host.startswith("/")
    if socket:
        query = {**query, "host": host}
    return URL.create(
        "postgresql+asyncpg",
        username=user,
        password=password,
        host=None if socket else host,
        port=port,
        database=database,
        query=query,
    ).render_as_string(hide_password=False)


{% endif -%}
def _is_origin(value: str) -> bool:
    """A scheme of http or https, a host and an optional port — and no path, query, fragment,
    credentials or wildcard."""
    try:
        parts = urlsplit(value)
        _ = parts.port  # raises on a port that is not a number
    except ValueError:
        return False
    return (
        parts.scheme in ("http", "https")
        and bool(parts.hostname)
        and not parts.path
        and not parts.query
        and not parts.fragment
        and parts.username is None
        and "*" not in value
    )


@lru_cache
def get_settings() -> Settings:
    """Cached accessor.

    A test that changes the environment MUST call `get_settings.cache_clear()`, or it will debug a
    cached config for twenty minutes.
    """
    return Settings()
