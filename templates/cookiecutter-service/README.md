# cookiecutter-service

> **This is a published copy.** It is generated from Athena's source and rewritten on every publish, so an edit made here is lost at the next one. Report a problem as an issue on the repository you found it in.

A production-ready **FastAPI service in a monorepo-shaped repository**. It generates a repo whose
backend already satisfies the handbook's **MUST**s — layering, typed settings, async SQLAlchemy,
Alembic, multi-stage image, Cloud Run health endpoints, CI and an inert-until-configured CD
pipeline.

**It is backend-only.** A browser client is a repository of its own, generated from
[`cookiecutter-react-app`](../cookiecutter-react-app), with its own stack and its own CD (ADR-0022),
and pointed at this API by two stack settings — see [A browser client](#a-browser-client).

**With a database, the endpoints it ships are the two health probes and six real authentication
endpoints** — register, login, refresh, logout, me, change password. They are the worked example of
the layering, and they are also a feature every service eventually needs, which is the point: there
is nothing here named after a thing no product has, waiting to be renamed or deleted. Session
handling is complete — rotating refresh tokens, reuse detection, logout and revocation — and
authorization is not: no roles, no permission guards, no ownership checks. `backend/AGENTS.md` says
so in as many words, and the handbook's JWT guide is where the rest of it is.

**With `use_postgres=no` the endpoints are the two health probes and nothing else.** Authentication
needs a user table; a service with no tables cannot have one, and half an auth system is worse than
none.

The reference implementation it was built from is `apps/handbook-mcp`, including the bugs that were
expensive to find there and are pre-fixed here.

## Use it

```bash
uvx cookiecutter gh:hashaaamm/athena-plugin --directory templates/cookiecutter-service   # interactive
uvx cookiecutter --no-input gh:hashaaamm/athena-plugin --directory templates/cookiecutter-service \
  project_name="Billing API"

# The same service, deploying to AWS instead: ECS Express Mode, and RDS when it has a database
uvx cookiecutter --no-input gh:hashaaamm/athena-plugin --directory templates/cookiecutter-service \
  project_name="Billing API" deploy_target=aws-ecs-express aws_account_id=123456789012 aws_region=eu-west-1
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
| `project_slug` | derived | Directory name, package name, the deployed service's name, compose project name. |
| `description` | … | One line. Lands in `pyproject.toml` and the OpenAPI description. |
| `author` | `Your Name` | Attribution. |
| `python_version` | `3.12` | Base image tag, `requires-python`, Ruff and mypy targets, `.python-version`. |
| `deploy_target` | `gcp-cloud-run` | Where the service deploys, and so which `infra/` and which deploy workflows the repository gets. `gcp-cloud-run` is Cloud Run, Cloud SQL and Artifact Registry, with state in GCS. `aws-ecs-express` is ECS Express Mode on Fargate, RDS for PostgreSQL and ECR, with state in S3 under AWS KMS (ADR-0021). Both are complete implementations of the same interface — the same `just infra-*` recipes, the same order of operations in `infra/README.md`, deploy pipelines that are inert until `just infra-sync-github` has run — so nothing outside `infra/` and `.github/workflows/` differs but the words. The unchosen target is deleted at generation. |
| `gcp_project_id` | `your-gcp-project` | `gcp-cloud-run` only. The GCP project the Pulumi stack targets: `infra/pulumi/Pulumi.dev.yaml`, the state-bucket URL in `Pulumi.yaml`, and the bootstrap script's default. Not in any workflow. |
| `gcp_region` | `europe-west1` | `gcp-cloud-run` only. Same three places. Cloud Run, Cloud SQL and Artifact Registry all live here. |
| `aws_account_id` | `123456789012` | `aws-ecs-express` only, and refused before generation unless it is 12 digits. The account the provider is allowed to touch (`allowed_account_ids`, so a shell holding another account's session fails before it plans anything), the state bucket `pulumi-state-<account>-<region>` in `Pulumi.yaml`, and the account the bootstrap script checks it is talking to. Not in any workflow. |
| `aws_region` | `eu-west-1` | `aws-ecs-express` only, and refused unless it reads as a region code. The same places; the VPC, the Express services, RDS and ECR all live here. |
| `github_repository` | `your-org/<slug>` | `owner/repo`. On GCP it becomes the Workload Identity provider's attribute condition; on AWS, the `sub` condition in the CI roles' trust policies. Either way it is the one line stopping any repository on GitHub from assuming the CI identity. Wrong here means a deploy that cannot authenticate; a wildcard here means anyone can. |
| `use_postgres` | `yes` | `no` drops Alembic, the ORM base, the database from Compose, readiness' database check **and the whole authentication example**, leaving the layer packages and the health endpoints. `yes` is the variant with two tables, two migrations and six working endpoints over them. |
| `use_sentry` | `yes` | `no` drops the SDK and `app/core/observability.py`. `yes` wires it with an **empty DSN by default** — local development reports nothing, by rule. |

## What is at the root, and what is in `backend/`

The rule applied throughout: **anything that orchestrates more than one component, or that a
developer runs from the repository root, is root. Anything specific to the Python service is
`backend/`.**

| At the root | Why |
| --- | --- |
| `justfile` | It is the entry point an engineer and an agent both reach for. It owns the stack (`dev`, `down`, `logs`) and delegates the rest to `backend/justfile`. Another component adds a delegation line, not a new command vocabulary. |
| `docker-compose.yml` / `.ci.yml` | They describe a *stack*: database plus backend. A compose file inside `backend/` cannot express "the backend waits for the database", and two compose files cannot share one network without ceremony. |
| `.github/workflows/` | One repository has one Actions surface. Per-component isolation is achieved with `paths:` filters inside the workflows, not by hiding workflow files in subdirectories — GitHub only reads `.github/workflows/` at the root anyway. |
| `.env.example` | The same `POSTGRES_*` values configure the `db` container *and* the backend that connects to it. Compose reads `.env` from its own directory, which is the root. |
| `infra/` | It describes where the *repository* runs, not how the Python service is built. It has its own `pyproject.toml`, its own lockfile and its own workflow, and it would be wrong inside `backend/` for the same reason `docker-compose.yml` is. |
| `.gitignore`, `.editorconfig`, `.pre-commit-config.yaml`, `.gitleaks.toml` | Repository-wide by definition. Pre-commit hooks are scoped with `files: ^backend/`; gitleaks reads its config from the repository root and nowhere else, in CI and in the hook alike. |
| `README.md`, `AGENTS.md` | The root `AGENTS.md` is a **map**; each subtree carries its own rules. the project structure rules MUST exactly this for monorepos — one root file cannot carry two stacks and stay under 100 lines. |

| In `backend/` | Why |
| --- | --- |
| `pyproject.toml`, `uv.lock`, `.python-version` | Python toolchain configuration. A root-level `pyproject.toml` would make `uv` treat the whole repository as the project. |
| `justfile` | Owns the language-specific recipes. Runs from `backend/`, and points at the root compose files with `-f ../docker-compose.yml`. |
| `app/`, `tests/`, `alembic/`, `alembic.ini` | The service and its migrations. |
| `docker/Dockerfile.{dev,prod}` | The build context is `backend/`, so the image never sees `infra/`, the workflows or anything else at the root. |
| `.importlinter` | Its contracts are about Python layers. |
| `.dockerignore` | Applies to the backend's build context. |
| `AGENTS.md` | The rules an agent needs while editing the service. |

## A browser client

A web client is generated from [`cookiecutter-react-app`](../cookiecutter-react-app) into a
repository of its own, with its own stack and CD, and calls this API across origins. Nothing in the
generated backend changes to allow it; two runtime settings do, and the stack sets both from
`Pulumi.<stack>.yaml`:

| Stack config | Becomes | What it does |
| --- | --- | --- |
| `corsOrigins` | `CORS_ORIGINS` on the service, when non-empty | The origins a browser may call this API from — the React app's `site_url`. Each entry must be a bare origin (`https://app.example.com`: no path, no trailing slash, no `*`); the stack and `Settings` both refuse anything else. Empty, the default, means no CORS at all. |
| `refreshTransport` | `REFRESH_TRANSPORT` on the service | `body`, the default, or `cookie`. With a database only, because without one there is no authentication. `cookie` mounts the cookie versions of login, refresh, logout and change-password: the refresh token becomes an `HttpOnly` cookie, refresh and logout require the CSRF header, and the OpenAPI document describes that transport. It needs `corsOrigins`, and both the stack and the service refuse it without one. |

Neither reaches the migration job: they are HTTP settings, and the job serves no HTTP.

**A session that survives a reload needs the app and the API on one site** — your own domain, with
the app and the API on two of its subdomains. The hostnames a deploy gets without one are two sites:
`run.app` is on the Public Suffix List, so two Cloud Run URLs are cross-site, and on AWS a
`cloudfront.net` app and an `on.aws` API are too. The refresh cookie is `SameSite=Strict`, so across
that line signing in works and a reload signs you out.

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

- **Layers, enforced.** `api → services → repositories → models`, with `import-linter`
  contracts that fail CI on a violation, an ORM ban outside the data layer and an HTTP-types ban
  inside it.
- **One composition point.** `app/api/deps.py` builds the graph; tests override the session there.
  The session dependency is `scope="function"`, so the request's commit runs before the response is
  sent and a commit that fails is a 500 — with FastAPI's default scope it would run after a 2xx had
  already gone. That is why `fastapi>=0.121` is the floor.
- **Typed settings.** One `Settings`, an `lru_cache`d accessor, no `os.environ` anywhere else, and
  a validator that refuses a placeholder password outside `local` and `test`. The JWT signing key
  is checked the same way but in `create_app` rather than in that validator — see "The signing
  key" below for why the difference is deliberate. `CORS_ORIGINS` and `REFRESH_TRANSPORT` are
  settings too, validated the same way and named as the Gin template names them — see "A browser
  client" above.
- **Async SQLAlchemy 2.0 + Alembic**, with an `env.py` that gets two things right that are easy to
  get wrong: the `context.begin_transaction()` block (without it the DDL runs and is discarded,
  and Alembic reports success over an empty database) and a URL — or a whole connection — read
  from `config.attributes` before Settings (so a test can inject one). A naming convention on
  `Base.metadata`, so every constraint and index has a name a `downgrade()` can drop. Two revisions,
  `0001_create_users` and `0002_create_refresh_sessions`: the tables the authentication endpoints
  are built on, and the root every later revision hangs off.
- **A multi-stage production image**: lockfile before source, a production stage that starts from a
  fresh slim base rather than inheriting the build toolchain, UID 1000, a HEALTHCHECK, and
  gunicorn + uvicorn workers with `WEB_CONCURRENCY` and `$PORT` from the environment. The start
  command is `backend/docker/start.sh`, not a string in the Dockerfile: a container's start command
  is read far more often than it is written, and the `exec`, the `$PORT` and the
  `--graceful-timeout` that make Cloud Run drain rather than kill are explained beside the flags.
- **Cloud Run shape**: separate `/health/live` and `/health/ready`, `NullPool` plus
  `prepared_statement_cache_size=0` for Cloud SQL, structured JSON logs, graceful shutdown.
- **Authentication, as the worked example.** Argon2id through `pwdlib`, HS256 access and refresh
  tokens through `PyJWT`, refresh tokens that rotate on every use and revoke their whole family when
  one comes back, a bearer dependency that is the only place 401 is decided, and a router split
  that makes a new route authenticated by default. Six endpoints, and the list of what it does
  *not* do is in `backend/AGENTS.md` where somebody will read it.
- **A real suite**: per-worker schema isolation, per-test transaction rollback, and tests that run
  through the whole stack to a real Postgres — including a decoder test per vulnerability class,
  because a suite that only decodes tokens the service minted proves the happy path and nothing
  about the arguments to `jwt.decode`; a two-connection test that the rotation lock really blocks;
  a write whose commit fails, answered with a 500 rather than a 201; and every migration run up,
  down and against the models — constraint names included — in a schema that is rolled back after.

### The authentication example, and where it stops

Six endpoints: `POST /api/v1/auth/register`, `POST /api/v1/auth/login`,
`POST /api/v1/auth/refresh`, `POST /api/v1/auth/logout`, `GET /api/v1/auth/me` and
`POST /api/v1/auth/change-password`. They exist because the template needed one worked resource
and this is the one every service ends up writing anyway — so unlike a toy `items` CRUD, nothing
about it is waiting to be renamed or deleted.

It is the handbook's JWT guide, implemented as far as those six endpoints reach — everything up to
the permission guard:

| | |
| --- | --- |
| Passwords | Argon2id via `pwdlib`, at `PasswordHash.recommended()` — m=64 MiB, t=3, p=4. Hashed on a worker thread, because 50-100 ms of CPU inside an `async def` is 50-100 ms nothing else on that event loop runs, readiness probe included. |
| Unknown accounts | Verified against a throwaway digest anyway, so a miss costs what a hit costs. A login endpoint that is fast for unknown addresses is a user directory. |
| Hash migration | `verify_and_update` on every login, persisted only when pwdlib returns a new digest. Login is the only moment the plaintext is in hand. |
| Tokens | HS256 via `PyJWT`. `sub`, `typ`, `iss`, `aud`, `iat`, `exp`, `jti`. An access token lasts fifteen minutes; a refresh token thirty days, carries no permissions, and is refused as a bearer token. |
| Decoding | An algorithm allowlist, required claims, issuer and audience checked, and `typ` compared — four arguments that are each a documented vulnerability class when they are left out. |
| Sessions | A `refresh_sessions` row per refresh token, keyed by its `jti`; the token itself is never stored. Every refresh revokes the token it spent and issues the next one in the same family, with the row locked `FOR UPDATE`. A spent token coming back revokes the whole family. |
| Reuse detection that survives its own 401 | A refused request rolls its writes back — except the family revocation, which is the point of the refusal. `CompromisedSessionError` is the one error the session dependency commits on. |
| Logout and revocation | Logout revokes the family and never fails. Changing a password revokes every family the user has and hands the caller a new one. A deactivated user's next refresh is refused and its family revoked. Revocation latency is the access token's fifteen minutes, and the service docstring says so. |
| Where the refresh token travels | `REFRESH_TRANSPORT`, at runtime. `body`, the default: in the JSON body, both ways. `cookie`, for a browser: in an `HttpOnly`, `Secure`, `SameSite=Strict` cookie scoped to `/api/v1/auth`, never in a body, with refresh and logout behind a custom-header and `Origin` CSRF check. `app/api/v1/auth_body.py` and `auth_cookie.py` hold the four routes that differ; `app/api/router.py` mounts one of them. |
| 401 | Decided in exactly one place, `get_current_actor`. `HTTPBearer(auto_error=False)`, because `auto_error=True` answers **403** to a missing header, in FastAPI's error shape rather than yours. |
| Closed by default | `app/api/router.py` mounts a `public` router and a `private` one carrying the actor dependency. A resource included in the wrong one is a mistake visible in one short function. |

**What it does not do**, said here rather than discovered later: no roles or permission guards, no
ownership checks, no password reset, no email verification, no rate limit on the login route, and
no scheduled job deleting expired `refresh_sessions` rows. An access token is good until it
expires, so revoking a session leaves the access token it already minted working for up to fifteen
minutes. Roles and ownership are the last two steps of the handbook's JWT guide.

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

### The migrations

Two revisions: `0001_create_users`, the root, and `0002_create_refresh_sessions` on top of it.

`alembic upgrade head` on a fresh database creates `users`, `refresh_sessions` and
`alembic_version`. `tests/test_migrations.py` runs every revision up, checks the result against the
models with `alembic check`, runs it down one and back up, and down to base — on a connection of its
own, in a schema it creates and rolls back, so it never touches the database's real schema. The
first `alembic revision --autogenerate` writes a child of `0002` rather than a competing root.

`just db-seed` is still the one recipe that does not ship. The only account table is `users`, and a
seeded account whose password is printed in a template is a back door in every project generated
from it. `backend/AGENTS.md` says what bringing it back looks like, with the first real resource.

### What the layering shows you now

`api -> services -> repositories -> models`, enforced by `import-linter` in `just lint` and in CI,
and demonstrated end to end by running code: `app/api/v1/auth.py` -> `AuthService` ->
`UserRepository` -> `app/models/user.py`. The service returns Pydantic schemas, so no ORM instance
reaches a view; `UserRepository` holds every query; and `HealthRepository.ping()` still keeps its
`SELECT 1` out of the route, because SQL outside the data layer is the convenient exception the
contracts exist to refuse.

With `use_postgres=no` none of that exists: no model, no repository, no auth, and view -> service
is the whole flow. That variant is health-only and says so.

## What the generated `infra/` gives you

A Pulumi stack in Python that has never been applied and is ready to be. What follows describes the
`gcp-cloud-run` target; `aws-ecs-express` has the same shape in AWS's terms, below the table.

On GCP it creates the service
APIs, an Artifact Registry repository with a cleanup policy, a runtime service account and a CI
service account with no downloadable key between them, a Workload Identity pool scoped to one
repository, Secret Manager containers, and a Cloud Run service that scales to zero — plus Cloud SQL
and a migration job when `use_postgres` is `yes`. `corsOrigins` and `refreshTransport` in the stack
configuration are how a browser client generated from `cookiecutter-react-app` is let in; see
[A browser client](#a-browser-client).

Five properties are worth knowing before you change any of it:

| Property | Why it is that way |
| --- | --- |
| The state bucket is a `gcloud` script, not a stack | A stack cannot create the store that holds its own state. `infra/bootstrap/state-bucket.sh` runs once, by a human. |
| Secret *containers* are in code; secret *values* are not | The stack creates an empty container and grants access; a person runs `gcloud secrets versions add`. A generated value — the database DSN — is the exception, and nobody ever types it. |
| The pipeline owns the image tag and nothing else | Declared `ignore_changes`, and every `just infra-*` recipe passes `--refresh`. Without the refresh, state still holds the old image, there is no diff to ignore, and the next apply rolls the service back to it. |
| Components take a frozen config, and are unit-tested | `just infra-test` runs with no cloud credentials at all. That is most of the argument for Pulumi over HCL, so it is the cheapest job in `infra.yml`. |
| Two settings are called "environment" | `environment` names resources; `appEnvironment` is the `ENVIRONMENT` variable the application reads — one of `local`/`test`/`staging`/`production`, defaulting to `staging`. They are configured separately because they vary separately, and conflating them is how a stack called `dev` deploys a service that believes it is `production`, serves no `/docs`, and says nothing about why. |
| Anything the stack can derive is shipped wired; anything needing an account, a bill, a domain or a human eye is documented instead | That line is drawn explicitly in the generated `infra/README.md`. A deployment step on the first side that a user has to write by hand is a bug in this template. |

**With `deploy_target=aws-ecs-express`** the stack creates a VPC in Express Mode's own shape —
public subnets for the load balancer and the tasks, no NAT gateway, isolated subnets with no route
out for the database — an ECS cluster, an ECR repository, CloudWatch log groups with a
retention, Secrets Manager secrets referenced by pinned `VersionId`, an Express service that
provisions its own HTTPS load balancer, and five IAM roles: infrastructure, execution and runtime for
ECS, and `deploy` and `preview` for CI, both trusting GitHub's OIDC token for exactly this
repository. With `use_postgres` it adds RDS PostgreSQL 16 behind `rds.force_ssl` and a Fargate
migration task CD runs before every deploy. The bootstrap script creates the S3 state bucket, its
KMS key and the account's GitHub OIDC provider — all three account-level and shared, so the stack
only looks them up — and a React app deployed into the same account shares them. Express Mode does not
scale to zero: one task and the load balancer bill while idle, and the generated `infra/README.md`
has the cost table.

`infra/README.md` in the generated repository is the ordered list of what a human has to do, from
installing `gcloud` or `aws` to setting branch protection, and it marks the step where money starts. It also
says why `just infra-up` prompts and how to run it unattended — `just infra-up dev --yes`. The
recipe passes flags through rather than passing `--yes` itself, because the one command in this
repository that creates billable resources should not auto-approve.

### The secret scan, and the one false positive it ships with

`pulumi stack init --secrets-provider=gcpkms://…` (or `awskms://…`) writes `secretsprovider` and `encryptedkey` into
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
