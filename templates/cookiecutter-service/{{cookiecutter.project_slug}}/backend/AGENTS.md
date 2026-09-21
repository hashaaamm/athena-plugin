# Agent rules — backend

Read [../AGENTS.md](../AGENTS.md) and the handbook's `AGENTS.md` first.

## Stack

| | |
| --- | --- |
| Framework | FastAPI, async end to end |
{%- if cookiecutter.use_postgres == "yes" %}
| Database | Postgres via SQLAlchemy 2.0 (async) + Alembic |
{%- endif %}
| Runtime | Cloud Run, region `{{ cookiecutter.gcp_region }}`, project `{{ cookiecutter.gcp_project_id }}` |
{%- if cookiecutter.use_sentry == "yes" %}
| Error tracking | Sentry (DSN empty locally, by rule) |
{%- endif %}

## Layout

```
app/
├── api/deps.py      # THE object graph; api/v1/ holds one router module per resource
├── core/            # config, database, logging, exceptions{% if cookiecutter.use_postgres == "yes" %}, security/{% endif %}{% if cookiecutter.use_sentry == "yes" %}, observability{% endif %}
├── services/        # one method per use case; business rules; ORM -> schema;
│                    # raise AppError, never HTTPException
├── repositories/    # every query lives here; flush(), never commit()
├── models/
├── schemas/
└── main.py          # app factory + lifespan
```

The flow is one-directional: `View -> Service -> Repository -> Model -> DB`. Never skip a layer,
never call backwards. A view makes exactly one service call, and what it calls returns a Pydantic
response schema — an ORM instance never reaches the view layer. `just lint` runs `lint-imports`,
which fails on a violation — the contracts are in `.importlinter`.

{% if cookiecutter.use_postgres == "yes" -%}
**Authentication is the worked example, and it runs through all four layers.**
`app/api/v1/auth.py` binds input and calls one method on `AuthService`; the service owns every
rule about passwords and enumeration and hands back a Pydantic schema; `UserRepository` owns every
query; `app/models/user.py` is the table. Read it before you write the second resource, because it
is the shape the second one takes. It is also a real feature rather than a placeholder — four
endpoints, `register`, `login`, `me` and `change-password` — so nothing about it is waiting to be
deleted.

What it deliberately leaves out, so you do not assume it is there: no refresh tokens, no logout,
no revocation, no roles or permission guards, no password reset, no email verification. An access
token is good until it expires and nothing can take it back. Ask Athena for the JWT authentication
guide before adding any of them; each is a step of it, and the revocation half needs a table.
{%- else -%}
The only endpoints shipped are the health probes, so read the layering as a rule rather than as
something the code demonstrates end to end. With no database there is no repository and no model:
`HealthService` has no dependencies, and view -> service is all the flow there is to see. There is
no authentication either — a user table is what a password is checked against, and this service
has no tables at all. The first resource you add is what makes the rest of it real.
{%- endif %}

## Rules specific to this service

1. Nothing commits except the session dependency in `app/core/database.py`. Repositories `flush()`.
2. No `os.environ` outside `app/core/config.py`. Read settings through `get_settings()`.
3. No blocking call inside an `async def`. Use `httpx.AsyncClient` with an explicit timeout, or
   `asyncio.to_thread` for CPU-bound work.
4. Every route declares `response_model`, an accurate `status_code`, and error `responses`.
{%- if cookiecutter.use_postgres == "yes" %}
5. A password digest never leaves the data layer. It is not on a response schema, not in a log
   line, not in an error message, and `User` gets no `__repr__` that would put it in one.
6. A new router goes on `private` in `app/api/router.py` unless there is a stated reason it is
   open. Deny by default is enforced there, not route by route.
7. `<<Add the rules that are specific to this domain. They are the highest-value lines in this
   file — "all money is Decimal", "every query filters by tenant_id".>>`
{%- else %}
5. `<<Add the rules that are specific to this domain. They are the highest-value lines in this
   file — "all money is Decimal", "every query filters by tenant_id".>>`
{%- endif %}

## Where to start when adding a resource

{% if cookiecutter.use_postgres == "yes" %}Read the auth resource end to end first — it is five files and it is all of them. Then write the
same five for yours, in this order, and skip none of them:

1. `app/models/<x>.py` — the table. Inherits `Base`, `UUIDMixin`, `TimestampMixin`, and is
   imported in `app/models/__init__.py` or autogenerate will not see it.
2. `app/schemas/<x>.py` — the wire contracts. `ConfigDict(from_attributes=True)` on anything a
   service builds from an ORM instance.
3. `app/repositories/<x>_repository.py` — `class XRepository(BaseRepository[X])`. Every query,
   and nothing else. `flush()`, never `commit()`.
4. `app/services/<x>_service.py` — one public method per use case, returning the response schema.
   Raises `AppError` subclasses.
5. `app/api/v1/<x>.py` — routes that bind input and make exactly one service call, plus the two
   providers in `app/api/deps.py` and **one `include_router` line in `app/api/router.py`, on the
   `private` router unless you can say out loud why an anonymous caller may have this**.

Then `just db-revision "create <x>"`, check the generated migration by eye, and write the tests:
the service's rules directly, the router through the HTTP client.

Development data does not ship: `just db-seed` and `app/seed.py` are gone, because the only table
here is `users` and a seeded account with a password printed in a repository is a back door in
every project generated from this template. Bring them back with your first real resource —
`app/seed.py` as an entry point that opens a session and calls one service method, no domain logic
and no shortcut around the service, plus a `db-seed` recipe in `backend/justfile` and a delegation
line in the root one. Seed the awkward cases, not just a happy row.{% else %}There is no example resource to copy — with no database the template ships the health endpoints
and nothing else, so the first one is written rather than renamed. It is the same files every
time, in this order, and none of them may be skipped:

1. `app/schemas/<x>.py` — the wire contracts, in and out.
2. `app/services/<x>_service.py` — one public method per use case, returning the response schema.
   Raises `AppError` subclasses, never `HTTPException`.
3. `app/api/v1/<x>.py` — routes that bind input and make exactly one service call, plus the
   `include_router` line in `app/api/router.py` and the provider in `app/api/deps.py`.

A repository and a model join that list the moment this service gets a database — which is a
`use_postgres=yes` regeneration, not a hand-rolled session. Authentication arrives with it, which
is the other thing a database turns on here.{% endif %}
