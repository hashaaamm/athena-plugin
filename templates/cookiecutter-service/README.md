# cookiecutter-service

> **Authored in [Engineering-Athena](https://github.com/hashaaamm/Engineering-Athena), under `templates/cookiecutter-service/`.** If you are reading this in the plugin repository you are reading a copy: `just sync-plugin` deletes this tree and rewrites it from source, so an edit made here is lost at the next publish. Change it there.

A production-ready **FastAPI service in a monorepo-shaped repository**, with an optional
**React + TypeScript SPA** beside it. It generates a repo whose backend already satisfies the
handbook's **MUST**s — layering, typed settings, async SQLAlchemy, Alembic, multi-stage image,
Cloud Run health endpoints, CI and an inert-until-configured CD pipeline — and whose frontend,
when you ask for one, arrives with a typed API client generated from that backend's own OpenAPI
document.

**With a database, the endpoints it ships are the two health probes and four real authentication
endpoints** — register, login, me, change password. They are the worked example of the layering,
and they are also a feature every service eventually needs, which is the point: there is nothing
here named after a thing no product has, waiting to be renamed or deleted. What they are not is a
complete auth system. No refresh tokens, no revocation, no roles. `backend/AGENTS.md` says so in
as many words, and the handbook's JWT guide is where the rest of it is.

**With `use_postgres=no` the endpoints are the two health probes and nothing else.** Authentication
needs a user table; a service with no tables cannot have one, and half an auth system is worse than
none.

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

### Into a repository that already exists

Cookiecutter writes a directory and **fails if that directory is already there** — including when
"already there" means a repository somebody created on GitHub five minutes ago that holds a
`README.md`, a `LICENSE` and an agent's dotfiles. There is no flag for it; generate beside the
target and move the tree in.

```bash
uvx cookiecutter --no-input -o /tmp/gen templates/cookiecutter-service project_name="Billing API"
rsync -a --exclude .git /tmp/gen/billing-api/ ./billing-api-repo/     # trailing slashes matter
```

That is the whole procedure **only when the target is effectively empty** — nothing at the root
that the template also writes. The moment it holds a `justfile`, a `docker-compose.yml`, a
`.github/workflows/` or a second service, this becomes a merge with collisions to reconcile, and
`rsync` will happily overwrite the side that was there first. Ask Athena for **merging a generated
service into a repository that already has code**; it is a step in the FastAPI guide, and the
companion knowledge page lists what collides and which collision is silent.

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
| `use_postgres` | `yes` | `no` drops Alembic, the ORM base, the database from Compose, readiness' database check **and the whole authentication example**, leaving the layer packages and the health endpoints. `yes` is the variant with a table, a migration and four working endpoints over it. |
| `use_sentry` | `yes` | `no` drops the SDK and `app/core/observability.py`. `yes` wires it with an **empty DSN by default** — local development reports nothing, by rule. |
| `include_frontend` | `no` | `yes` scaffolds the React SPA: Vite, TanStack Router and Query, Tailwind v4 with shadcn/ui, a generated API client, Vitest, its own justfile, Compose service, production image, CI *and* CD workflows, and its own Cloud Run service in the Pulumi stack — and turns on CORS in the backend, pointed at that service's URL. Its one page is a dashboard over `/health/ready`; it does **not** sign anybody in, and there is no token in `localStorage`, because where a browser keeps a credential is a decision this template will not make for you. `no` removes the directory, both its workflows and the CORS test entirely. |

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
| `.gitignore`, `.editorconfig`, `.pre-commit-config.yaml`, `.gitleaks.toml` | Repository-wide by definition. Pre-commit hooks are scoped with `files: ^backend/`; gitleaks reads its config from the repository root and nowhere else, in CI and in the hook alike. |
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
template into a scratch directory with `include_frontend=yes` and copy across five things:

1. `frontend/` itself.
2. The `frontend` service and the `frontend-node-modules` volume in `docker-compose.yml`.
3. The `frontend :=` line in the root `justfile`, plus the delegation lines in `lint`, `fmt`,
   `test`, `build` and the `gen-api` recipe.
4. `.github/workflows/frontend-ci.yml`, the `VITE_*` and `CORS_ORIGINS` block in `.env.example`,
   and the CORS middleware in `backend/app/main.py` with its setting and its test.
5. Its half of the deployment: the second `Service(...)` and its two exports in
   `infra/pulumi/__main__.py`, the `web` runtime account in `components/identities.py`, the two
   frontend rows in `infra/scripts/sync-github.sh`, and
   `.github/workflows/frontend-cd.yml`. The unit test in `infra/pulumi/tests` that asserts those
   three name the same variables is what tells you if you copied two of the three.

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
  a validator that refuses a placeholder password outside `local` and `test`. The JWT signing key
  is checked the same way but in `create_app` rather than in that validator — see "The signing
  key" below for why the difference is deliberate.
- **Async SQLAlchemy 2.0 + Alembic**, with an `env.py` that gets two things right that are easy to
  get wrong: the `context.begin_transaction()` block (without it the DDL runs and is discarded,
  and Alembic reports success over an empty database) and a URL read from `config.attributes`
  before Settings (so a test can inject one). One revision, `0001_create_users`, which is the
  table the authentication endpoints are built on and the root every later revision hangs off.
- **A multi-stage production image**: lockfile before source, a production stage that starts from a
  fresh slim base rather than inheriting the build toolchain, UID 1000, a HEALTHCHECK, and
  gunicorn + uvicorn workers with `WEB_CONCURRENCY` and `$PORT` from the environment. The start
  command is `backend/docker/start.sh`, not a string in the Dockerfile: a container's start command
  is read far more often than it is written, and the `exec`, the `$PORT` and the
  `--graceful-timeout` that make Cloud Run drain rather than kill are explained beside the flags.
- **Cloud Run shape**: separate `/health/live` and `/health/ready`, `NullPool` plus
  `prepared_statement_cache_size=0` for Cloud SQL, structured JSON logs, graceful shutdown.
- **Authentication, as the worked example.** Argon2id through `pwdlib`, HS256 access tokens
  through `PyJWT`, a bearer dependency that is the only place 401 is decided, and a router split
  that makes a new route authenticated by default. Four endpoints, and the list of what it does
  *not* do is in `backend/AGENTS.md` where somebody will read it.
- **A real suite**: per-worker schema isolation, per-test transaction rollback, and tests that run
  through the whole stack to a real Postgres — including a decoder test per vulnerability class,
  because a suite that only decodes tokens the service minted proves the happy path and nothing
  about the arguments to `jwt.decode`.

### The authentication example, and where it stops

Four endpoints: `POST /api/v1/auth/register`, `POST /api/v1/auth/login`, `GET /api/v1/auth/me`,
`POST /api/v1/auth/change-password`. They exist because the template needed one worked resource
and this is the one every service ends up writing anyway — so unlike a toy `items` CRUD, nothing
about it is waiting to be renamed or deleted.

It is the handbook's JWT guide, implemented as far as those four endpoints reach:

| | |
| --- | --- |
| Passwords | Argon2id via `pwdlib`, at `PasswordHash.recommended()` — m=64 MiB, t=3, p=4. Hashed on a worker thread, because 50-100 ms of CPU inside an `async def` is 50-100 ms nothing else on that event loop runs, readiness probe included. |
| Unknown accounts | Verified against a throwaway digest anyway, so a miss costs what a hit costs. A login endpoint that is fast for unknown addresses is a user directory. |
| Hash migration | `verify_and_update` on every login, persisted only when pwdlib returns a new digest. Login is the only moment the plaintext is in hand. |
| Tokens | HS256 via `PyJWT`. `sub`, `typ`, `iss`, `aud`, `iat`, `exp`, `jti`; fifteen minutes. |
| Decoding | An algorithm allowlist, required claims, issuer and audience checked, and `typ` compared — four arguments that are each a documented vulnerability class when they are left out. |
| 401 | Decided in exactly one place, `get_current_actor`. `HTTPBearer(auto_error=False)`, because `auto_error=True` answers **403** to a missing header, in FastAPI's error shape rather than yours. |
| Closed by default | `app/api/router.py` mounts a `public` router and a `private` one carrying the actor dependency. A resource included in the wrong one is a mistake visible in a five-line file. |

**What it does not do**, said here rather than discovered later: no refresh tokens, no logout, no
revocation, no roles or permission guards, no password reset, no email verification, no rate limit
on the login route. An access token is good until it expires; changing a password does not end a
session, and deactivating a user does not either. Each of those is a step of the handbook's JWT
guide, and the revocation half needs a table of its own.

### The signing key

`JWT_SECRET`, typed as a `SecretStr` in `Settings`, with **no usable default**. The local sentinel
is a sentinel: a deployed server refuses to start with it, or with any key under 32 characters —
which is RFC 7518's floor for HS256 and also the length below which PyJWT warns.

That check lives in `app/core/security/tokens.py` and runs from `create_app`, not in the settings
validator, and the difference matters: the migration job builds `Settings` too, is deliberately
granted no signing key because it signs nothing, and a check in the validator would stop it
booting. The template's own comment on that validator says exactly this, so following it is
following the house rule rather than the guide's literal placement.

Deployed, the Pulumi stack mints 48 random characters into Secret Manager and mounts them on the
service alone. Nobody types the value and nobody needs to read it.

### The migration

One revision, `0001_create_users`, and it replaced the empty `0001_baseline` that existed only
because there were no tables. Now there is one, so a second revision that creates nothing would be
a file every generated project deletes.

`alembic upgrade head` on a fresh database creates `users` and `alembic_version`. `alembic check`
reports no drift, `alembic downgrade base` works and has been run, and the first
`alembic revision --autogenerate` writes a child of `0001` rather than a competing root.

`just db-seed` is still the one recipe that does not ship. The only table is `users`, and a seeded
account whose password is printed in a template is a back door in every project generated from it.
`backend/AGENTS.md` says what bringing it back looks like, with the first real resource.

### What the layering shows you now

`api -> services -> repositories -> models`, enforced by `import-linter` in `just lint` and in CI,
and demonstrated end to end by running code: `app/api/v1/auth.py` -> `AuthService` ->
`UserRepository` -> `app/models/user.py`. The service returns Pydantic schemas, so no ORM instance
reaches a view; `UserRepository` holds every query; and `HealthRepository.ping()` still keeps its
`SELECT 1` out of the route, because SQL outside the data layer is the convenient exception the
contracts exist to refuse.

With `use_postgres=no` none of that exists: no model, no repository, no auth, and view -> service
is the whole flow. That variant is health-only and says so.

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
- **One page, and no sign-in flow.** The dashboard reads `/health/ready` and renders loading,
  error and data — three of the four states every fetch has. The backend's auth endpoints are in
  the generated types, so a login form compiles the moment somebody writes one, but this template
  puts no token in `localStorage` and ships no auth flow: where a browser keeps a credential is a
  decision with consequences and it is not a scaffold's to make. The form stack
  (`react-hook-form`, `zod`, `@hookform/resolvers`) is installed and is what
  `frontend/README.md` prescribes, so the first form is a component rather than a dependency
  argument.
- **Vitest + Testing Library**, asserting through roles and text, mocking at the `api` client
  rather than at `fetch`.
- **A production image** that builds with pnpm and serves with `nginx-unprivileged` on `$PORT`,
  with the SPA fallback and cache headers written down — plus its own paths-filtered CI
  workflow, so a backend change never queues behind a frontend runner.
- **The other half of that: somewhere to run it.** A second Cloud Run service in
  `infra/pulumi/__main__.py` with its own runtime account, no database and no secrets, and a
  `frontend-cd.yml` that builds the image, tags it with the commit and updates that one field —
  inert until `WIF_PROVIDER` is set, like every other pipeline here.
- **CORS in the backend**, configured rather than wildcarded, with tests. Without it the API
  answers every request correctly and the browser still refuses to hand the body to the page.

Two constraints are worth knowing before you edit any of it. `VITE_*` variables are **inlined at
build time**, so they are Docker build args, not runtime environment variables, and none of them
can be a secret. And the lockfile is not templated, for the same reason `uv.lock` is not.

The first of those is the reason the deploy pipeline has an ordering rather than just steps: the
API's origin is compiled into the bundle, so `frontend-cd.yml` reads it from the `SERVICE_URL`
repository variable the stack exports, and refuses to build when it has neither that nor the
`FRONTEND_API_URL` override. A bundle built without one calls `http://localhost:8000`, deploys
cleanly, serves a page and answers nothing — which is why it is a failed run instead.

## What the generated `infra/` gives you

A Pulumi stack in Python that has never been applied and is ready to be. It creates the service
APIs, an Artifact Registry repository with a cleanup policy, a runtime service account and a CI
service account with no downloadable key between them, a Workload Identity pool scoped to one
repository, Secret Manager containers, and a Cloud Run service that scales to zero — plus Cloud SQL
and a migration job when `use_postgres` is `yes`, and a second Cloud Run service with a runtime
account of its own for the SPA when `include_frontend` is `yes`.

Five properties are worth knowing before you change any of it:

| Property | Why it is that way |
| --- | --- |
| The state bucket is a `gcloud` script, not a stack | A stack cannot create the store that holds its own state. `infra/bootstrap/state-bucket.sh` runs once, by a human. |
| Secret *containers* are in code; secret *values* are not | The stack creates an empty container and grants access; a person runs `gcloud secrets versions add`. A generated value — the database DSN — is the exception, and nobody ever types it. |
| The pipeline owns the image tag and nothing else | Declared `ignore_changes`, and every `just infra-*` recipe passes `--refresh`. Without the refresh, state still holds the old image, there is no diff to ignore, and the next apply rolls the service back to it. |
| Components take a frozen config, and are unit-tested | `just infra-test` runs with no cloud credentials at all. That is most of the argument for Pulumi over HCL, so it is the cheapest job in `infra.yml`. |
| Two settings are called "environment" | `environment` names resources; `appEnvironment` is the `ENVIRONMENT` variable the application reads — one of `local`/`test`/`staging`/`production`, defaulting to `staging`. They are configured separately because they vary separately, and conflating them is how a stack called `dev` deploys a service that believes it is `production`, serves no `/docs`, and says nothing about why. |
| Anything the stack can derive is shipped wired; anything needing an account, a bill, a domain or a human eye is documented instead | That line is drawn explicitly in the generated `infra/README.md`. A deployment step on the first side that a user has to write by hand is a bug in this template, and the frontend's missing Cloud Run service was one. |

`infra/README.md` in the generated repository is the ordered list of what a human has to do, from
installing `gcloud` to setting branch protection, and it marks the step where money starts. It also
says why `just infra-up` prompts and how to run it unattended — `just infra-up dev --yes`. The
recipe passes flags through rather than passing `--yes` itself, because the one command in this
repository that creates billable resources should not auto-approve.

### The secret scan, and the one false positive it ships with

`pulumi stack init --secrets-provider=gcpkms://…` writes `secretsprovider` and `encryptedkey` into
`Pulumi.<stack>.yaml`. `encryptedkey` is a base64 blob, gitleaks' `generic-api-key` rule cannot
tell it from a credential, and the first push of a generated repository fails on it. So the
template ships `.gitleaks.toml`, read by both the CI job and the pre-commit hook.

Two things about it are deliberate and both are easy to get wrong. It sets `[extend] useDefault =
true` — **a gitleaks config without that replaces the default rules with nothing**, so every scan
reports "no leaks found" and the gate passes by no longer looking. And it allowlists by *line*
rather than by path: a `paths` entry makes gitleaks skip the file before it reads it, which would
hide a real credential somebody pasted into the stack file by hand.

## Related

- [templates/README.md](../README.md) · [templates/agent-rules/](../agent-rules)
- The rules a generated repository is built to satisfy are retrieved, not linked: ask Athena for
  **starting a new service**, **layered architecture**, **project structure**, **containerization**,
  **local development and the command runner**, **GitHub Actions standards**, **Cloud Run service
  standards** and the **Pulumi standards**. Nothing in a generated repository points at a file path
  in this one, because a generated repository does not have this one.
