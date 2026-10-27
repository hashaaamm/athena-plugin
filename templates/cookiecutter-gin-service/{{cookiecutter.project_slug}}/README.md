# {{ cookiecutter.project_name }}

{{ cookiecutter.description }}

Generated from the Engineering Athena `cookiecutter-gin-service` template. The handbook is the
authority on every convention below; this file only says where things are.

## Run it

```bash
cp .env.example .env                    # `just dev` does this for you if you forget
(cd backend && just lock)               # once: resolve go.sum, which is not templated
just dev                                # API on http://localhost:8000/docs
```

```bash
just check      # everything CI runs
just test       # the suite, against a real Postgres
just --list     # every recipe, with its doc comment
```

Every backend recipe runs in a container, so Docker and `just` are all the backend needs. The
Pulumi program under `infra/` is the exception: it needs Go and the `pulumi` CLI on your machine.

## Layout

```
.
├── justfile                 # orchestrator: delegates to backend/justfile and infra/justfile
├── docker-compose.yml       # the whole stack
├── docker-compose.ci.yml
├── .github/workflows/       # ci.yml, cd.yml, infra.yml — paths-filtered per component
├── .env.example             # stack-wide configuration
├── .gitleaks.toml           # secret-scan config: default rules, plus two allowlisted lines
├── infra/                   # Pulumi (Go): the cloud resources — see infra/README.md
│   ├── bootstrap/           # plain {% if cookiecutter.deploy_target == "aws-ecs-express" %}aws CLI{% else %}gcloud{% endif %}; creates the Pulumi state bucket, and removes it again
│   ├── scripts/             # copies stack outputs into GitHub repository variables
│   └── pulumi/              # the program, its components and their unit tests
└── backend/                 # the Gin service
    ├── cmd/api/             # one binary: serve, migrate, healthcheck
    ├── internal/{app,config,http,service,repository,db,security,apperr}
    ├── migrations/          # Goose SQL, embedded in the binary; also sqlc's schema
    ├── queries/             # SQL that sqlc turns into typed Go
    ├── docs/                # generated Swagger and OpenAPI documents
    ├── docker/              # the dev (hot reload) and production images
    ├── justfile             # backend-only recipes
    └── go.mod
```

**Root or `backend/`?** Anything that orchestrates more than one component, or that a developer
runs from the repository root, lives at the root. Anything specific to the Go service lives in
`backend/`. `infra/` is a sibling of `backend/` for the same reason: it describes where the whole
repository runs, not how the service is built, and it has its own module and its own workflow.

## The backend, in one paragraph

{% if cookiecutter.use_postgres == "yes" %}It answers `/health/live`, `/health/ready`, and six authentication endpoints:
`POST /api/v1/auth/register`, `POST /api/v1/auth/login`, `POST /api/v1/auth/refresh`,
`POST /api/v1/auth/logout`, `GET /api/v1/auth/me` and `POST /api/v1/auth/change-password`. They are
a real feature and the worked example of the layering at the same time — handler, service,
repository, generated query. Sessions are complete: refresh tokens rotate on every use, a reused one
revokes its whole session, logout and a password change end sessions for real. Authorization is
not: there are no roles and no ownership checks. `backend/AGENTS.md` says what that costs you and
where to read next.{% else %}The endpoints it answers today are `/health/live` and `/health/ready`, and nothing else. With no
database there is nothing to authenticate against either. There is no example resource to
rename — `backend/AGENTS.md` lists the files the first one takes.{% endif %}

Requests flow one way: `Handler → Service → Repository → generated query → DB`. Handlers bind input
and call exactly one service method. A service method is the whole use case and returns typed
errors from `internal/apperr`, never an HTTP status, so it stays callable from a job or a CLI.
Repositories are the only code that touches `internal/db`. `internal/app` is the only place the
object graph is assembled. `just lint` enforces the import rules mechanically with
golangci-lint's `depguard`.

## The HTTP contract, and browser clients

This backend answers the same HTTP contract as the FastAPI service template — routes, status codes,
field names, the error envelope, the refresh cookie and the OpenAPI document at `/openapi.json`.
This repository is backend only. A browser client is generated from the handbook's
`cookiecutter-react-app` template into a repository of its own, with its own stack and pipelines,
and pointed at this API: its `API_URL` is this stack's `service_url`, and this stack's
`corsOrigins` lists its `site_url`.{% if cookiecutter.use_postgres == "yes" %} Where the refresh token travels is a setting, `REFRESH_TRANSPORT`
(`refreshTransport` in the stack): `body` for API clients, `cookie` for a browser, with the CSRF
check and CORS that a cookie needs. A browser session survives a reload only with the client and
the API on one site — a domain you own; on the default hostnames, signing in works and a reload
signs out.{% endif %} `infra/README.md` has the steps.

## Deploying

{% if cookiecutter.deploy_target == "aws-ecs-express" -%}
`infra/` declares the AWS resources in Pulumi: an ECS Express Mode service behind its own HTTPS
load balancer{% if cookiecutter.use_postgres == "yes" %}, RDS for PostgreSQL{% endif %}, and the IAM roles CI assumes without a stored
credential. `.github/workflows/cd.yml` builds an immutable `:sha` image{% if cookiecutter.use_postgres == "yes" %}, runs the migrations as a
one-off ECS task,{% endif %} and updates one field on the service — the image. It is **inert until
configured**: the job is gated on the repository variable `AWS_DEPLOY_ROLE_ARN`, which the stack
exports and `just infra-sync-github` sets.
{%- else -%}
`infra/` declares the cloud resources in Pulumi. `.github/workflows/cd.yml` builds an immutable
`:sha` image, runs the migrations as a Cloud Run job, and updates one field on the service — the
image tag. It is **inert until configured**: every job is gated on the repository variable
`WIF_PROVIDER`, which the stack exports and `just infra-sync-github` sets.
{%- endif %}

**[infra/README.md](infra/README.md) is the order it has to happen in**, and it draws the line
between what this repository ships ready to run and what only you can do — {% if cookiecutter.deploy_target == "aws-ecs-express" %}an AWS account{% else %}a GCP project{% endif %} with
billing, reading the preview before the first apply, populating the secret {% if cookiecutter.deploy_target == "aws-ecs-express" %}values{% else %}containers{% endif %}, a domain,
and branch protection.

```bash
just infra-test           # component tests; no cloud credentials
just infra-preview        # what `up` would change
just infra-up             # creates real, billable resources; prompts, by design
just infra-up dev --yes   # the same, unattended — pulumi refuses to guess
just infra-sync-github    # copies stack outputs into repository variables
just infra-destroy        # removes every resource the stack created
just infra-teardown       # then the state bucket and KMS key, which the stack never created
```

**Taking it down is two commands, and the second one is not optional.** `pulumi destroy` removes
what the stack created; the state bucket and the KMS key{% if cookiecutter.deploy_target == "aws-ecs-express" %} — and the account's GitHub
OIDC provider —{% endif %} came from the bootstrap script before the
stack existed, so Pulumi has never known about them. `just infra-teardown` removes them, and
refuses while any stack still holds a resource.
{%- if cookiecutter.deploy_target == "aws-ecs-express" %}

**Nothing here scales to zero.** Express Mode runs at least one task behind a load balancer that
bills by the hour{% if cookiecutter.use_postgres == "yes" %}, and RDS bills by the hour whether or not anyone connects{% endif %}. The settings are
already the cheapest ones that work; `infra/README.md` says what each one is and what the floor
costs you.
{%- elif cookiecutter.use_postgres == "yes" %}

**Cloud SQL is the only thing here that bills while idle**, and its settings are already the
cheapest ones that work. `infra/README.md` says what each one is and what the floor costs you.
{%- endif %}

## Configuration, and where each value lives

Three places, and the distinction is the security boundary. Nothing moves between them.

| Kind | Set in | Read by | Examples |
| --- | --- | --- | --- |
| Local development | `.env`, copied from `.env.example` and never committed | Compose and the service on your machine | `ENVIRONMENT`, `JSON_LOGS`{% if cookiecutter.use_postgres == "yes" %}, `POSTGRES_*`, `REFRESH_TRANSPORT`{% endif %} |
| Deploy configuration | GitHub repository **variables** | `.github/workflows/cd.yml` | {% if cookiecutter.deploy_target == "aws-ecs-express" %}`AWS_DEPLOY_ROLE_ARN`, `AWS_REGION`, `IMAGE_REPO`, `ECS_CLUSTER`, `ECS_SERVICE_ARN`, `ECS_MIGRATE_TASK_FAMILY`{% else %}`WIF_PROVIDER`, `DEPLOY_SA`, `GCP_REGION`, `IMAGE_REPO`, `CLOUD_RUN_SERVICE`, `CLOUD_RUN_MIGRATE_JOB`{% endif %} |
| Runtime secrets | {% if cookiecutter.deploy_target == "aws-ecs-express" %}Secrets Manager | the ECS task, as an environment variable pinned to one version |{% else %}Secret Manager | the Cloud Run revision, as an environment variable |{% endif %} {% if cookiecutter.use_postgres == "yes" %}`DATABASE_URL_OVERRIDE`, `JWT_SECRET`{% endif %}{% if cookiecutter.use_postgres == "yes" and cookiecutter.use_sentry == "yes" %}, {% endif %}{% if cookiecutter.use_sentry == "yes" %}`SENTRY_DSN`{% endif %} |

{% if cookiecutter.use_postgres == "yes" %}The stack generates the first two and nobody ever types either: the database DSN, and `JWT_SECRET`
— 48 random characters minted by Pulumi, mounted on the service and on nothing else. The migration
{% if cookiecutter.deploy_target == "aws-ecs-express" %}task{% else %}job{% endif %} is deliberately not granted the signing key; it signs nothing. A deployed server refuses to
start with the local sentinel key, or with one under 32 characters.{% endif %}{% if cookiecutter.use_sentry == "yes" %}
`SENTRY_DSN` is the one a human fills in, from {% if cookiecutter.deploy_target == "aws-ecs-express" %}a secret{% else %}a container{% endif %} the stack creates empty.{% endif %}

No secret value belongs in a file in this repository. `gitleaks` enforces that on every push and in
a pre-commit hook, reading `.gitleaks.toml` at the root. That file extends the default rule set
rather than replacing it, and allowlists two *lines* — Pulumi's `encryptedkey` and
`secretsprovider`, which are KMS ciphertext and a key URL. If a finding is a false positive,
allowlist the line, never the path.

## Related

- [infra/README.md](infra/README.md) — the deployment order and what only a human can do
- `AGENTS.md` — the map for coding agents; `backend/AGENTS.md` for the service itself
- Ask Athena for the rules this repository is built to: the Gin standards, backend layering,
  containerization, GitHub Actions, {% if cookiecutter.deploy_target == "aws-ecs-express" %}{% else %}Cloud Run and {% endif %}Pulumi. Retrieve them by topic rather than by
  path — the handbook is served against the question you actually have.
