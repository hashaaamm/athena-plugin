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
├── core/            # config, database, logging, exceptions{% if cookiecutter.use_sentry == "yes" %}, observability{% endif %}
├── facades/         # one per resource; ORM -> schema; one method per use case
├── services/        # business rules; raise AppError, never HTTPException
├── repositories/    # every query lives here; flush(), never commit()
├── models/
├── schemas/
└── main.py          # app factory + lifespan
```

The flow is one-directional: `View -> Facade -> Service -> Repository -> Model -> DB`. Never skip a
layer, never call backwards. `just lint` runs `lint-imports`, which fails on a violation — the
contracts are in `.importlinter`.

## Rules specific to this service

1. Nothing commits except the session dependency in `app/core/database.py`. Repositories `flush()`.
2. No `os.environ` outside `app/core/config.py`. Read settings through `get_settings()`.
3. No blocking call inside an `async def`. Use `httpx.AsyncClient` with an explicit timeout, or
   `asyncio.to_thread` for CPU-bound work.
4. Every route declares `response_model`, an accurate `status_code`, and error `responses`.
5. `<<Add the rules that are specific to this domain. They are the highest-value lines in this
   file — "all money is Decimal", "every query filters by tenant_id".>>`

## Where to start when adding a resource

Copy `item` end to end — model, schema, repository, service, facade, router, tests — rename, and
delete what you do not need. Then delete the `item` example itself once a real resource exists.
