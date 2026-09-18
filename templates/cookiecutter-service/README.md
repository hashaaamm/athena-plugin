# cookiecutter-service

A production-ready **FastAPI service in a monorepo-shaped repository**. It generates a repo whose
backend already satisfies the handbook's **MUST**s — layering, typed settings, async SQLAlchemy,
Alembic, multi-stage image, Cloud Run health endpoints, CI and an inert-until-configured CD
pipeline — and whose *shape* leaves room for a frontend beside it.

The reference implementation it was built from is `apps/handbook-mcp`, including the bugs that were
expensive to find there and are pre-fixed here.

## Use it

```bash
uvx cookiecutter templates/cookiecutter-service        # interactive
uvx cookiecutter --no-input templates/cookiecutter-service project_name="Billing API"
```

Then, in the generated repository:

```bash
cd <slug>/backend && uv lock && cd ..   # resolve the lockfile the images build from
just dev                                # http://localhost:8000/docs
just check                              # everything CI runs

cd infra/pulumi && uv lock && cd ../..  # the stack has its own lockfile, for the same reason
just infra-test                         # the component tests; no cloud credentials needed
```

The lockfile is **not** templated. A committed `uv.lock` would be stale on the day it was written
and would pin whatever versions existed when this template was last touched; resolving once at
generation is the honest alternative. Every Dockerfile installs with `uv sync --frozen`, so a stale
lockfile fails the build loudly rather than silently resolving something new.

## Variables

| Variable | Default | What it does |
| --- | --- | --- |
| `project_name` | `Example Service` | Human name. Used in the OpenAPI title and the READMEs. |
| `project_slug` | derived | Directory name, package name, Cloud Run service name, compose project name. |
| `description` | … | One line. Lands in `pyproject.toml` and the OpenAPI description. |
| `author` | `Your Name` | Attribution. |
| `python_version` | `3.12` | Base image tag, `requires-python`, Ruff and mypy targets, `.python-version`. |
| `gcp_project_id` | `your-gcp-project` | The GCP project the Pulumi stack targets: `infra/pulumi/Pulumi.dev.yaml`, the state-bucket URL in `Pulumi.yaml`, and the bootstrap script's default. Not in any workflow. |
| `gcp_region` | `europe-west1` | Same three places. Cloud Run, Cloud SQL and Artifact Registry all live here. |
| `github_repository` | `your-org/<slug>` | `owner/repo`. Becomes the Workload Identity provider's attribute condition, which is the one line stopping any repository on GitHub from assuming the CI identity. Wrong here means a deploy that cannot authenticate; a wildcard here means anyone can. |
| `use_postgres` | `yes` | `no` drops Alembic, the ORM, the example resource and the database from Compose, leaving the layer packages and the health endpoints. |
| `use_sentry` | `yes` | `no` drops the SDK and `app/core/observability.py`. `yes` wires it with an **empty DSN by default** — local development reports nothing, by rule. |
| `include_frontend` | `no` | `yes` scaffolds a `frontend/` placeholder with its own justfile, `AGENTS.md` and README. `no` removes the directory entirely. |

## What is at the root, and what is in `backend/`

The rule applied throughout: **anything that orchestrates more than one component, or that a
developer runs from the repository root, is root. Anything specific to the Python service is
`backend/`.**

| At the root | Why |
| --- | --- |
| `justfile` | It is the entry point an engineer and an agent both reach for. It owns the stack (`dev`, `down`, `logs`) and delegates the rest to `backend/justfile`. Adding a frontend adds a delegation line, not a new command vocabulary. |
| `docker-compose.yml` / `.ci.yml` | They describe a *stack*: database plus backend, later plus frontend. A compose file inside `backend/` cannot express "the frontend waits for the backend", and two compose files cannot share one network without ceremony. |
| `.github/workflows/` | One repository has one Actions surface. Per-component isolation is achieved with `paths:` filters inside the workflows, not by hiding workflow files in subdirectories — GitHub only reads `.github/workflows/` at the root anyway. |
| `.env.example` | The same `POSTGRES_*` values configure the `db` container *and* the backend that connects to it. Compose reads `.env` from its own directory, which is the root. |
| `infra/` | It describes where the *repository* runs, not how the Python service is built. It has its own `pyproject.toml`, its own lockfile and its own workflow, and it would be wrong inside `backend/` for the same reason `docker-compose.yml` is. |
| `.gitignore`, `.editorconfig`, `.pre-commit-config.yaml` | Repository-wide by definition. Pre-commit hooks are scoped with `files: ^backend/`. |
| `README.md`, `AGENTS.md` | The root `AGENTS.md` is a **map**; each subtree carries its own rules. the project structure rules MUST exactly this for monorepos — one root file cannot carry two stacks and stay under 100 lines. |

| In `backend/` | Why |
| --- | --- |
| `pyproject.toml`, `uv.lock`, `.python-version` | Python toolchain configuration. A frontend has no opinion about Ruff, and a root-level `pyproject.toml` would make `uv` treat the whole repository as the project. |
| `justfile` | Owns the language-specific recipes. Runs from `backend/`, and points at the root compose files with `-f ../docker-compose.yml`. |
| `app/`, `tests/`, `alembic/`, `alembic.ini` | The service and its migrations. |
| `docker/Dockerfile.{dev,prod}` | The build context is `backend/`, so the image never sees the frontend's `node_modules`. |
| `.importlinter` | Its contracts are about Python layers. |
| `.dockerignore` | Applies to the backend's build context. |
| `AGENTS.md` | The rules an agent needs while editing the service. |

### Adding a frontend later

1. `mkdir frontend` (or generate with `include_frontend=yes` up front) and scaffold inside it.
2. Add a `frontend` service to the root `docker-compose.yml` — the commented block shows where.
3. Add `frontend := "just --justfile frontend/justfile --working-directory frontend"` to the root
   justfile and one line to `lint`, `fmt` and `test`.
4. Add `.github/workflows/frontend-ci.yml` with `paths: ['frontend/**']`, rather than more jobs in
   `ci.yml` — separate workflows mean neither component queues behind the other's runner.
5. Deploying it is one more `Service(...)` in `infra/pulumi/__main__.py`, with its own runtime
   service account. The template ships no Cloud Run service for `frontend/` because there is no
   image to run in it; a service permanently on the bootstrap placeholder is a resource that
   survives for years because nothing ever fails.

Nothing in `backend/` moves.

## The cookiecutter trap this template handles explicitly

GitHub Actions' `${{ ... }}` and Jinja's `{{ ... }}` are the same two braces. Rendering a workflow
file through cookiecutter silently strips every expression in it, and the damage shows up as a
mysteriously broken pipeline in a repository nobody has run yet.

`cookiecutter.json` therefore declares:

```json
"_copy_without_render": [".github/workflows/*"]
```

The consequence is a real constraint, not a free win: **no file under `.github/workflows/` may
contain a cookiecutter variable.** Everything project-specific in those workflows comes from
repository variables (`vars.CLOUD_RUN_SERVICE`, `vars.IMAGE_REPO`, `vars.GCP_REGION`) instead. That
is also why CD is inert until configured — an unset `vars.WIF_PROVIDER` skips every job, and the
same gate keeps `infra.yml`'s preview job dormant until the stack exists.

Those variable names are not a convention anyone has to remember. `infra/pulumi` exports each one
and `infra/scripts/sync-github.sh` copies it in, so the two halves cannot drift — and a unit test in
`infra/pulumi/tests/` fails if they do. A stack exporting `workload_identity_provider` to a pipeline
reading `vars.WIF_PROVIDER` is a deploy job that is skipped forever and never says why, which is
exactly the class of failure a template should make impossible rather than document.

The same collision exists in `just`, whose interpolation syntax is *also* `{{ }}`. The justfiles
are rendered, so their bodies are wrapped in `{% raw %}` blocks with the cookiecutter conditionals
sitting outside them. If you edit a justfile in this template, check you are still inside the right
block.

## What the generated backend gives you

- **Layers, enforced.** `api → facades → services → repositories → models`, with `import-linter`
  contracts that fail CI on a violation, an ORM ban outside the data layer and an HTTP-types ban
  inside it.
- **One composition point.** `app/api/deps.py` builds the graph; tests override the session there.
- **Typed settings.** One `Settings`, an `lru_cache`d accessor, no `os.environ` anywhere else, and
  a validator that refuses a placeholder password outside `local` and `test`.
- **Async SQLAlchemy 2.0 + Alembic**, with an `env.py` that gets two things right that are easy to
  get wrong: the `context.begin_transaction()` block (without it the DDL runs and is discarded,
  and Alembic reports success over an empty database) and a URL read from `config.attributes`
  before Settings (so a test can inject one).
- **A multi-stage production image**: lockfile before source, a production stage that starts from a
  fresh slim base rather than inheriting the build toolchain, UID 1000, a HEALTHCHECK, and
  gunicorn + uvicorn workers with `WEB_CONCURRENCY` and `$PORT` from the environment.
- **Cloud Run shape**: separate `/health/live` and `/health/ready`, `NullPool` plus
  `prepared_statement_cache_size=0` for Cloud SQL, structured JSON logs, graceful shutdown.
- **A real suite**: per-worker schema isolation, per-test transaction rollback, and tests that run
  through the whole stack to a real Postgres.

## What the generated `infra/` gives you

A Pulumi stack in Python that has never been applied and is ready to be. It creates the service
APIs, an Artifact Registry repository with a cleanup policy, a runtime service account and a CI
service account with no downloadable key between them, a Workload Identity pool scoped to one
repository, Secret Manager containers, and a Cloud Run service that scales to zero — plus Cloud SQL
and a migration job when `use_postgres` is `yes`.

Four properties are worth knowing before you change any of it:

| Property | Why it is that way |
| --- | --- |
| The state bucket is a `gcloud` script, not a stack | A stack cannot create the store that holds its own state. `infra/bootstrap/state-bucket.sh` runs once, by a human. |
| Secret *containers* are in code; secret *values* are not | The stack creates an empty container and grants access; a person runs `gcloud secrets versions add`. A generated value — the database DSN — is the exception, and nobody ever types it. |
| The pipeline owns the image tag and nothing else | Declared `ignore_changes`, and every `just infra-*` recipe passes `--refresh`. Without the refresh, state still holds the old image, there is no diff to ignore, and the next apply rolls the service back to it. |
| Components take a frozen config, and are unit-tested | `just infra-test` runs with no cloud credentials at all. That is most of the argument for Pulumi over HCL, so it is the cheapest job in `infra.yml`. |

`infra/README.md` in the generated repository is the ordered list of what a human has to do, from
installing `gcloud` to setting branch protection, and it marks the step where money starts.

## Related

- [templates/README.md](../README.md) · [templates/agent-rules/](../agent-rules)
- The rules a generated repository is built to satisfy are retrieved, not linked: ask Athena for
  **starting a new service**, **layered architecture**, **project structure**, **containerization**,
  **local development and the command runner**, **GitHub Actions standards**, **Cloud Run service
  standards** and the **Pulumi standards**. Nothing in a generated repository points at a file path
  in this one, because a generated repository does not have this one.
