# {{ cookiecutter.project_name }}

{{ cookiecutter.description }}

Generated from the Engineering Athena `cookiecutter-service` template. The handbook is the
authority on every convention below; this file only says where things are.

## Run it

```bash
cd backend && uv lock && cd ..   # once: resolve the lockfile the images build from
cp .env.example .env             # `just dev` does this for you if you forget
just dev                         # http://localhost:8000/docs
```

```bash
just check      # everything CI runs
just test       # parallel suite against a real Postgres
just --list     # every recipe, with its doc comment
```

## Layout

```
.
├── justfile                 # orchestrator: delegates to backend/ (and later frontend/)
├── docker-compose.yml       # the whole stack
├── docker-compose.ci.yml
├── .github/workflows/       # ci.yml, cd.yml, infra.yml — paths-filtered per component
├── .env.example             # stack-wide configuration
├── infra/                   # Pulumi (Python): the cloud resources — see infra/README.md
│   ├── bootstrap/           # plain gcloud; creates the Pulumi state bucket
│   ├── scripts/             # copies stack outputs into GitHub repository variables
│   └── pulumi/              # the stack, its components and their unit tests
├── backend/                 # the FastAPI service
│   ├── app/{api,facades,services,repositories,models,schemas,core}
│   ├── alembic/
│   ├── docker/
│   ├── tests/
│   ├── justfile             # backend-only recipes
│   └── pyproject.toml
{%- if cookiecutter.include_frontend == "yes" %}
└── frontend/                # placeholder — see frontend/README.md
{%- endif %}
```

**Root or `backend/`?** Anything that orchestrates more than one component, or that a developer
runs from the repository root, lives at the root. Anything specific to the Python service lives in
`backend/`. That is the whole rule, and it is why adding a frontend later is dropping in a
directory rather than restructuring this one. `infra/` is a sibling of `backend/` for the same
reason: it describes where the whole repository runs, not how the Python service is built, and it
has its own toolchain and its own workflow.

## The backend, in one paragraph

Requests flow one way: `View → Facade → Service → Repository → Model → DB`. Views bind input and
call exactly one facade method. Facades own a use case and convert entities to wire schemas.
Services own the business rules and raise `AppError` subclasses — never `HTTPException`, so they
stay callable from a worker or a CLI. Repositories own every query and `flush()`; the session
dependency is the only thing that commits. `app/api/deps.py` is the only place the object graph is
assembled, which makes it the only seam a test has to override. `just lint` enforces all of this
mechanically via `import-linter`.

## Deploying

`infra/` declares the cloud resources in Pulumi; `.github/workflows/cd.yml` builds an immutable
`:sha` image and updates one field on the Cloud Run service — the image tag. CD is **inert until
configured**: every job is gated on the repository variable `WIF_PROVIDER`, which the stack exports
and `just infra-sync-github` sets.

**[infra/README.md](infra/README.md) is the order it has to happen in**, including the four things
no script can do for you: a GCP project with billing, reading the preview before the first apply,
populating the secret containers, and branch protection.

```bash
just infra-test           # component tests; no cloud credentials
just infra-preview        # what `up` would change
just infra-up             # creates real, billable resources
just infra-sync-github    # copies stack outputs into repository variables
```

## Configuration, and where each value lives

Three places, and the distinction is the security boundary. Nothing moves between them.

| Kind | Set in | Read by | Examples |
| --- | --- | --- | --- |
| Local development | `.env`, copied from `.env.example` and never committed | Compose and the app on your machine | `ENVIRONMENT`, `JSON_LOGS`{% if cookiecutter.use_postgres == "yes" %}, `POSTGRES_*`{% endif %} |
| Deploy configuration | GitHub repository **variables** | `.github/workflows/cd.yml` | `WIF_PROVIDER`, `DEPLOY_SA`, `GCP_REGION`, `IMAGE_REPO`, `CLOUD_RUN_SERVICE`, `CLOUD_RUN_MIGRATE_JOB` |
| Runtime secrets | Secret Manager, populated by a human | the Cloud Run revision, as an environment variable | {% if cookiecutter.use_postgres == "yes" %}`DATABASE_URL_OVERRIDE`{% endif %}{% if cookiecutter.use_postgres == "yes" and cookiecutter.use_sentry == "yes" %}, {% endif %}{% if cookiecutter.use_sentry == "yes" %}`SENTRY_DSN`{% endif %} |

Repository variables are deliberately variables and not secrets: a project id, a region and a
service-account email grant nothing on their own, and being readable in logs is what makes a failed
deploy debuggable. The one repository *secret* is `SENTRY_AUTH_TOKEN`, if you wire up release
markers.

No secret value belongs in a file in this repository. Not in code, not in a test fixture, not in a
Pulumi program, not in CI YAML, not in a tracked `.env`.

## Related

- [infra/README.md](infra/README.md) — the deployment order and what only a human can do
- `AGENTS.md` — the map for coding agents; `backend/AGENTS.md` for the service itself
- Ask Athena for the rules this repository is built to: backend layering, containerization,
  GitHub Actions, Cloud Run and Pulumi. Retrieve them by topic rather than by path — the handbook
  is served against the question you actually have.
