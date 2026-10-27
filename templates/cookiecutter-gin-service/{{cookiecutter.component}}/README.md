# {{ cookiecutter.project_name }}

{{ cookiecutter.description }}

Generated from the Engineering Athena `cookiecutter-gin-service` template as the `{{ cookiecutter.component }}`
component of its repository: this folder, plus its three workflows at the repository root
(ADR-0024). The handbook is the authority on every convention below; this file only says where
things are.

## Run it

From this folder — or from the repository root as `just {{ cookiecutter.component }} <recipe>`, which runs the same
recipe here:

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

The root justfile reaches this one through a `just` module (`mod {{ cookiecutter.component }}`), which needs `just` 1.31
or newer.

Every backend recipe runs in a container, so Docker and `just` are all the backend needs. The
Pulumi program under `infra/` is the exception: it needs Go and the `pulumi` CLI on your machine.

## Layout

```
<repository root>
├── justfile                 # `mod {{ cookiecutter.component }}`, one line per component
├── AGENTS.md                # lists the components
├── .github/workflows/       # {{ cookiecutter.component }}-ci.yml, -cd.yml, -infra.yml
└── {{ cookiecutter.component }}/  # this component
    ├── justfile             # orchestrator: delegates to backend/justfile and infra/justfile
    ├── docker-compose.yml   # the whole local stack, Compose project `{{ cookiecutter.__name }}`
    ├── docker-compose.ci.yml
    ├── .env.example         # stack-wide configuration
    ├── .gitleaks.toml       # secret-scan config: default rules, plus two allowlisted lines
    └── …                    # infra/ and backend/, below
```

```
{{ cookiecutter.component }}/
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

**This folder or `backend/`?** Anything that spans the component's parts — the local stack, CI's
gate, the deploy — lives in `{{ cookiecutter.component }}/`. Anything specific to the Go service lives in `backend/`.
`infra/` is a sibling of `backend/` for the same reason: it describes where the component runs, not
how the service is built, and it has its own module and its own workflow. Only what GitHub and
`just` look for at the repository root lives there, and it carries this component's name.

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
This component is backend only. A browser client is generated from the handbook's
`cookiecutter-react-app` template as another component of this repository — a folder of its own,
with its own stack and pipelines (ADR-0024) — and pointed at this API: its `apiUrl` is this stack's `service_url`, and this stack's
`corsOrigins` lists its `site_url`.{% if cookiecutter.use_postgres == "yes" %} Where the refresh token travels is a setting, `REFRESH_TRANSPORT`
(`refreshTransport` in the stack): `body` for API clients, `cookie` for a browser, with the CSRF
check and CORS that a cookie needs. A browser session survives a reload only with the client and
the API on one site: the React template serves this API under `/api/*` on the app's own origin by
default, and a domain you own is the production setup.{% endif %} `infra/README.md` has the steps.

## Deploying

{% if cookiecutter.deploy_target == "aws-ecs-express" -%}
`infra/` declares the AWS resources in Pulumi: an ECS Express Mode service behind its own HTTPS
load balancer{% if cookiecutter.use_postgres == "yes" %}, RDS for PostgreSQL{% endif %}, and the IAM roles CI assumes without a stored
credential. `.github/workflows/{{ cookiecutter.component }}-cd.yml` builds an immutable `:sha` image{% if cookiecutter.use_postgres == "yes" %}, runs the migrations as a
one-off ECS task,{% endif %} and updates one field on the service — the image. It is **inert until
configured**: the job is gated on the repository variable `{{ cookiecutter.__component_var }}_AWS_DEPLOY_ROLE_ARN`, which the
stack exports and `just infra-sync-github` sets.
{%- else -%}
`infra/` declares the cloud resources in Pulumi. `.github/workflows/{{ cookiecutter.component }}-cd.yml` builds an
immutable `:sha` image, runs the migrations as a Cloud Run job, and updates one field on the
service — the image tag. It is **inert until configured**: the job is gated on the repository
variable `{{ cookiecutter.__component_var }}_WIF_PROVIDER`, which the stack exports and `just infra-sync-github` sets.
{%- endif %}

**[infra/README.md](infra/README.md) is the order it has to happen in**, and it draws the line
between what this repository ships ready to run and what only you can do — {% if cookiecutter.deploy_target == "aws-ecs-express" %}an AWS account{% else %}a GCP project{% endif %} with
billing, reading the preview before the first apply, populating the secret {% if cookiecutter.deploy_target == "aws-ecs-express" %}values{% else %}containers{% endif %}, a domain,
and branch protection.

**The first image is built on your machine, not in CI.** `just infra-up` creates everything but
the service; `just infra-first-deploy` builds the production image from the committed tree, pushes
it, creates the service on it{% if cookiecutter.use_postgres == "yes" %}, runs the migrations{% endif %} and smoke-tests it. CD owns every image after that one.
There is no placeholder image.

```bash
just infra-test                # component tests; no cloud credentials
just infra-preview             # what `up` would change
just infra-up                  # creates real, billable resources — all but the service; prompts, by design
just infra-up dev --yes        # the same, unattended — pulumi refuses to guess
{%- if cookiecutter.use_postgres == "yes" %}
just infra-secrets             # generates the secrets the stack leaves empty
{%- endif %}
just infra-first-deploy        # builds and pushes the real image, creates the service on it; prompts too
just infra-sync-github         # copies stack outputs into repository variables, un-gating CD
just infra-destroy             # removes every resource the stack created
just infra-teardown            # then the state bucket and KMS key, which the stack never created
```

**Setup runs from your machine once; every later infrastructure change is applied by CI.** After
`just infra-sync-github`, `.github/workflows/{{ cookiecutter.component }}-infra.yml` previews every pull request that touches
`infra/` and applies the change when it merges to `main`, as the stack's apply {% if cookiecutter.deploy_target == "aws-ecs-express" %}role{% else %}service account{% endif %}, in the
`infrastructure` GitHub environment. Protect that environment — required reviewers, `main` only —
before the first push. `just infra-up` from a laptop after setup is the exception the Pulumi
standards' MUST-9 forbids.

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
| Local development | `.env`, copied from `.env.example` and never committed | Compose and the service on your machine | `ENVIRONMENT`, `JSON_LOGS`{% if cookiecutter.use_postgres == "yes" %}, `DATABASE_AUTH`, `POSTGRES_*`, `REFRESH_TRANSPORT`{% endif %} |
| Deploy configuration | GitHub repository **variables**, prefixed `{{ cookiecutter.__component_var }}_` | `.github/workflows/{{ cookiecutter.component }}-cd.yml` | {% if cookiecutter.deploy_target == "aws-ecs-express" %}`{{ cookiecutter.__component_var }}_AWS_DEPLOY_ROLE_ARN`, `{{ cookiecutter.__component_var }}_AWS_REGION`, `{{ cookiecutter.__component_var }}_IMAGE_REPO`, `{{ cookiecutter.__component_var }}_ECS_CLUSTER`, `{{ cookiecutter.__component_var }}_ECS_SERVICE_ARN`, `{{ cookiecutter.__component_var }}_ECS_MIGRATE_TASK_FAMILY`{% else %}`{{ cookiecutter.__component_var }}_WIF_PROVIDER`, `{{ cookiecutter.__component_var }}_DEPLOY_SA`, `{{ cookiecutter.__component_var }}_GCP_REGION`, `{{ cookiecutter.__component_var }}_IMAGE_REPO`, `{{ cookiecutter.__component_var }}_CLOUD_RUN_SERVICE`, `{{ cookiecutter.__component_var }}_CLOUD_RUN_MIGRATE_JOB`{% endif %} |
| Runtime secrets | {% if cookiecutter.deploy_target == "aws-ecs-express" %}Secrets Manager | the ECS task, as an environment variable pinned to one version |{% else %}Secret Manager | the Cloud Run revision, as an environment variable pinned to one version |{% endif %} {% if cookiecutter.use_postgres == "yes" %}`JWT_SECRET`{% if cookiecutter.deploy_target == "aws-ecs-express" %}; the migration task's `DATABASE_USER` and `DATABASE_PASSWORD`{% else %}; the migration job's `DATABASE_PASSWORD`{% endif %}{% endif %}{% if cookiecutter.use_postgres == "yes" and cookiecutter.use_sentry == "yes" %}; {% endif %}{% if cookiecutter.use_sentry == "yes" %}`SENTRY_DSN`{% endif %} |

{% if cookiecutter.use_postgres == "yes" -%}
No secret value is in the Pulumi program, its configuration or its state (ADR-0023). The stack
creates each secret empty and mounts it only once its version is recorded; `just infra-secrets`
generates the values, pipes them in, and records the versions — `JWT_SECRET` is 64 random
characters nobody sees, mounted on the service and on nothing else. A deployed server refuses to
start with the local sentinel key, or with one under 32 characters.

The deployed service holds **no database password at all**. It logs in as its own cloud identity
— `DATABASE_AUTH={% if cookiecutter.deploy_target == "aws-ecs-express" %}rds-iam`, with a token its task role signs for every new connection{% else %}cloud-sql-iam`, through the Cloud SQL Go connector{% endif %} — with row
privileges only. The migration {% if cookiecutter.deploy_target == "aws-ecs-express" %}task logs in as the RDS master user, from the secret RDS itself
creates and rotates{% else %}job logs in as Cloud SQL's `postgres` admin, with the password
`just infra-secrets` set{% endif %}, owns the schema, and after every run grants the service's role what it needs.
It is not granted the signing key; it signs nothing.
{%- endif %}{% if cookiecutter.use_sentry == "yes" %}
`SENTRY_DSN` is the one a human fills in, from {% if cookiecutter.deploy_target == "aws-ecs-express" %}a secret{% else %}a container{% endif %} the stack creates empty.{% endif %}

No secret value belongs in a file in this repository. `gitleaks` enforces that in CI — the CLI
from its own image, over every commit that touched this folder — and in a pre-commit hook, reading
`{{ cookiecutter.component }}/.gitleaks.toml`. That file extends the default rule set
rather than replacing it, and allowlists two *lines* — Pulumi's `encryptedkey` and
`secretsprovider`, which are KMS ciphertext and a key URL. If a finding is a false positive,
allowlist the line, never the path.

## Related

- [infra/README.md](infra/README.md) — the deployment order and what only a human can do
- `AGENTS.md` — the map for coding agents; `backend/AGENTS.md` for the service itself
- Ask Athena for the rules this repository is built to: the Gin standards, backend layering,
  containerization, GitHub Actions, {% if cookiecutter.deploy_target == "aws-ecs-express" %}{% else %}Cloud Run and {% endif %}Pulumi. Retrieve them by topic rather than by
  path — the handbook is served against the question you actually have.
