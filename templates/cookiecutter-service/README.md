# cookiecutter-service

> **Authored in [Engineering-Athena](https://github.com/hashaaamm/Engineering-Athena), under `templates/cookiecutter-service/`.** If you are reading this in the plugin repository you are reading a copy: `just sync-plugin` deletes this tree and rewrites it from source, so an edit made here is lost at the next publish. Change it there.

A production-ready **FastAPI service in a monorepo-shaped repository**, with an optional
**React + TypeScript SPA** beside it. It generates a repo whose backend already satisfies the
handbook's **MUST**s — layering, typed settings, async SQLAlchemy, Alembic, multi-stage image,
Cloud Run health endpoints, CI and an inert-until-configured CD pipeline — and whose frontend,
when you ask for one, arrives with a typed API client generated from that backend's own OpenAPI
document.

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
| `include_frontend` | `no` | `yes` scaffolds the React SPA: Vite, TanStack Router and Query, Tailwind v4 with shadcn/ui, a generated API client, Vitest, its own justfile, Compose service, CI workflow and production image — and turns on CORS in the backend. `no` removes the directory, its workflow and the CORS test entirely. |

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

### Adding the frontend later

Generating with `include_frontend=no` and changing your mind is not a reshuffle. Re-run the
template into a scratch directory with `include_frontend=yes` and copy across four things:

1. `frontend/` itself.
2. The `frontend` service and the `frontend-node-modules` volume in `docker-compose.yml`.
3. The `frontend :=` line in the root `justfile`, plus the delegation lines in `lint`, `fmt`,
   `test`, `build` and the `gen-api` recipe.
4. `.github/workflows/frontend-ci.yml`, the `VITE_*` and `CORS_ORIGINS` block in `.env.example`,
   and the CORS middleware in `backend/app/main.py` with its setting and its test.

Nothing in `backend/` moves, and nothing in `infra/` has to change until you want the SPA
deployed — which is one more `Service(...)` in `infra/pulumi/__main__.py` with its own runtime
service account. The template ships no Cloud Run service for the frontend because there is no
image in the registry to run in it, and a service permanently on the bootstrap placeholder is a
resource that survives for years because nothing ever fails.

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

**And a third time, in JSX.** An inline object prop — `activeProps={{ className: "..." }}` — opens
with the same two braces. Every one of those in `frontend/` is hoisted to a `const` above the
component, which is why the JSX in this template has no inline object props anywhere. It is also
better React, but that is not why it is done. Before committing an edit under `frontend/`, grep
the file for a doubled brace that is not a cookiecutter variable you meant to write.

## What the generated backend gives you

- **Layers, enforced.** `api → services → repositories → models`, with `import-linter`
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

## What the generated `frontend/` gives you

Only with `include_frontend=yes`. A React 19 SPA in TypeScript, built with Vite 6 — the same
stack, and largely the same files, as the reference application it was lifted from.

- **A generated API client.** `openapi-typescript` turns the backend's OpenAPI document into
  types and `openapi-fetch` makes the calls, so a renamed field is a failed build rather than
  `undefined` in a table cell. `just gen-api` regenerates it; the committed `schema.d.ts` is a
  hand-written seed so a fresh clone type-checks before the backend has ever been started.
- **One HTTP entry point**, in `src/lib/api/client.ts`, which also reads the backend's error
  envelope: `{ error: { code, message } }` becomes a thrown `ApiError` with the code intact.
- **Fetchers, then hooks over them.** Each resource module exports plain async functions and
  thin TanStack Query wrappers. Testing a fetcher needs no React and no provider, which is why
  the fetchers are the part with tests.
- **TanStack Router with the tree in one readable file**, a 404 screen, a router-level error
  screen, and an error boundary *inside* the shell so a page that throws costs the user the
  page rather than their navigation.
- **Tailwind v4 and shadcn/ui**, with the palette in `src/index.css` — there is no
  `tailwind.config.js` in v4, and no hex code belongs in a component.
- **A worked resource** (list, create with react-hook-form + zod, delete) that renders every
  state a request has: loading, empty, error, data. It exists to be read and then deleted, and
  it is removed automatically when `use_postgres=no` leaves no endpoint to call.
- **Vitest + Testing Library**, asserting through roles and text, mocking at the `api` client
  rather than at `fetch`.
- **A production image** that builds with pnpm and serves with `nginx-unprivileged` on `$PORT`,
  with the SPA fallback and cache headers written down — plus its own paths-filtered CI
  workflow, so a backend change never queues behind a frontend runner.
- **CORS in the backend**, configured rather than wildcarded, with tests. Without it the API
  answers every request correctly and the browser still refuses to hand the body to the page.

Two constraints are worth knowing before you edit any of it. `VITE_*` variables are **inlined at
build time**, so they are Docker build args, not runtime environment variables, and none of them
can be a secret. And the lockfile is not templated, for the same reason `uv.lock` is not.

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
