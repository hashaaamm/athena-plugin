# {{ cookiecutter.project_name }}

{{ cookiecutter.description }}

Generated from the Engineering Athena `cookiecutter-fastapi-service` template. The handbook is the
authority on every convention below; this file only says where things are.

## Run it

```bash
cp .env.example .env                    # `just dev` does this for you if you forget
(cd backend && uv lock)                 # once: resolve the lockfile the images build from
just dev                                # API on http://localhost:8000/docs
```

```bash
just check      # everything CI runs
just test       # parallel suite against a real Postgres
just --list     # every recipe, with its doc comment
```

## Layout

```
.
├── justfile                 # orchestrator: delegates to each component's justfile
├── docker-compose.yml       # the whole stack
├── docker-compose.ci.yml
├── .github/workflows/       # ci.yml, cd.yml, infra.yml — paths-filtered per component
├── .env.example             # stack-wide configuration
├── .gitleaks.toml           # secret-scan config: default rules, plus two allowlisted lines
├── infra/                   # Pulumi (Python): the cloud resources — see infra/README.md
│   ├── bootstrap/           {% if cookiecutter.deploy_target == "aws-ecs-express" %}# plain AWS CLI; creates the Pulumi state bucket, and removes it again{% else %}# plain gcloud; creates the Pulumi state bucket, and removes it again{% endif %}
│   ├── scripts/             # copies stack outputs into GitHub repository variables
│   └── pulumi/              # the stack, its components and their unit tests
└── backend/                 # the FastAPI service
    ├── app/{api,services,repositories,models,schemas,core}
    ├── alembic/
    ├── docker/              # the dev and production images, and the server's start script
    ├── tests/
    ├── justfile             # backend-only recipes
    └── pyproject.toml
```

**Root or `backend/`?** Anything that orchestrates more than one component, or that a developer
runs from the repository root, lives at the root. Anything specific to the Python service lives in
`backend/`. That is the whole rule. `infra/` is a sibling of `backend/` for the same reason: it
describes where the whole repository runs, not how the Python service is built, and it has its own
toolchain and its own workflow.

## The backend, in one paragraph

{% if cookiecutter.use_postgres == "yes" %}It answers `/health/live`, `/health/ready`, and six authentication endpoints:
`POST /api/v1/auth/register`, `POST /api/v1/auth/login`, `POST /api/v1/auth/refresh`,
`POST /api/v1/auth/logout`, `GET /api/v1/auth/me` and `POST /api/v1/auth/change-password`. They are
a real feature and the worked example of the layering at the same time — router, service,
repository, model. Sessions are complete: refresh tokens rotate on every use, a reused one revokes
its whole session, logout and a password change end sessions for real. Authorization is not: there
are no roles and no ownership checks. `backend/AGENTS.md` says what that costs you and where to read
next.{% else %}The endpoints it answers today are `/health/live` and `/health/ready`, and nothing else. With no
database there is nothing to authenticate against either. There is no example resource to
rename — `backend/AGENTS.md` lists the three files the first one takes.{% endif %}

Requests flow one way: `View → Service → Repository → Model → DB`. Views bind input and call
exactly one service method. A service method is the whole use case: it owns the business rules,
orchestrates whatever else the case needs, and returns the wire schema — so no ORM instance reaches
the view layer. Services raise `AppError` subclasses, never `HTTPException`, so they stay callable
from a worker or a CLI. Repositories own every query and `flush()`; the session dependency is the
only thing that commits. `app/api/deps.py` is the only place the object graph is
assembled, which makes it the only seam a test has to override. `just lint` enforces all of this
mechanically via `import-linter`.

## A browser client

This repository is the API and nothing else. A web client is a repository of its own, generated
from the Engineering Athena `cookiecutter-react-app` template, with its own stack and its own CD.
Pointing it at this API is configuration here, not code:

- **`corsOrigins`** in `infra/pulumi/Pulumi.<stack>.yaml` — the app's `site_url`, comma-separated if
  there is more than one. The stack sets it as `CORS_ORIGINS` on the service. Empty, the default,
  means no browser may call this API at all.
{%- if cookiecutter.use_postgres == "yes" %}
- **`refreshTransport: cookie`** in the same file — the stack sets it as `REFRESH_TRANSPORT`. The
  refresh token then travels as an httpOnly cookie that no script in the page can read, instead of
  in the JSON body, and refresh and logout require the CSRF header. It needs `corsOrigins`; the stack
  and the service both refuse it without one.

**A session that survives a reload needs the app and the API on one site** — your own domain, with
the app on `app.example.com` and the API on `api.example.com`. The hostnames you get without one are
two sites: {% if cookiecutter.deploy_target == "aws-ecs-express" %}a CloudFront app on `cloudfront.net` and this API on `on.aws`{% else %}`run.app` is on the Public Suffix List, so two Cloud Run URLs are two sites{% endif %}. The refresh cookie
is `SameSite=Strict`, so the browser never sends it across that line: signing in works, and a reload
signs you out.
{%- endif %}

Locally the app's dev server is `http://localhost:3000`: put that in `CORS_ORIGINS` in `.env`{% if cookiecutter.use_postgres == "yes" %}, and
set `REFRESH_TRANSPORT=cookie`{% endif %}.

## Deploying

`infra/` declares the cloud resources in Pulumi. `.github/workflows/cd.yml` builds an immutable
`:sha` image and {% if cookiecutter.deploy_target == "aws-ecs-express" %}updates one field on the backend's ECS Express service — the image tag{% else %}updates one field on the backend's Cloud Run service — the image tag{% endif %}. It is
**inert until configured**: {% if cookiecutter.deploy_target == "aws-ecs-express" %}every job is gated on a repository variable — `AWS_DEPLOY_ROLE_ARN` — which the{% else %}every job is gated on the repository variable `WIF_PROVIDER`, which the{% endif %}
stack exports and `just infra-sync-github` sets.

**[infra/README.md](infra/README.md) is the order it has to happen in**, and it draws the line
between what this repository ships ready to run and {% if cookiecutter.deploy_target == "aws-ecs-express" %}what only you can do — an AWS account with
billing,{% else %}what only you can do — a GCP project with
billing,{% endif %} reading the preview before the first apply, populating the secret containers, a domain,
and branch protection. Everything on the other side of that line is generated, wired and tested
here; if you find yourself writing deploy plumbing by hand, that is a bug in the template.

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
what the stack created; the state bucket and the KMS key came from `infra/bootstrap/state-bucket.sh`
before the stack existed, so Pulumi has never known about them and will never remove them.
`just infra-teardown` is the other end: it refuses while any stack still holds a resource — deleting
the bucket first strands the state that `destroy` needs — {% if cookiecutter.deploy_target == "aws-ecs-express" %}makes you type the bucket name, and schedules
the KMS key for deletion rather than deleting it — KMS waits 30 days, and until then the key can
be restored.{% else %}makes you type the bucket name, and prints
the KMS commands you cannot run for another 30 days, because a KMS key is scheduled for destruction
rather than deleted.{% endif %} [infra/README.md](infra/README.md) has the order and the reasons.
{%- if cookiecutter.use_postgres == "yes" %}

{% if cookiecutter.deploy_target == "aws-ecs-express" %}It is worth knowing before the first apply that **ECS Express Mode cannot scale to zero**: the load
balancer, one Fargate task per service and the RDS instance all bill whether or not anybody calls
them. The database settings are already the cheapest ones that work — `db.t4g.micro`, 20 GB of
gp3, single-AZ — and `infra/README.md` says what each one is and what the floor costs you.{% else %}It is worth knowing before the first apply that **Cloud SQL is the only thing here that bills while
idle**, and that its settings are already the cheapest ones that work — shared-core, the minimum
10 GB of HDD, zonal, no point-in-time recovery outside production. `infra/README.md` says what each
one is, what the floor costs you (no SLA), and what the only two levers below it give up.{% endif %}
{%- endif %}{%- if cookiecutter.deploy_target == "aws-ecs-express" and cookiecutter.use_postgres != "yes" %}

It is worth knowing before the first apply that **ECS Express Mode cannot scale to zero**: the load
balancer and one Fargate task per service bill whether or not anybody calls them. `infra/README.md`
has the cost table, and why there is no NAT gateway to add to it.
{%- endif %}

**Two things here are called "environment" and they are not the same one.**
`{{ cookiecutter.project_slug }}:environment` in `Pulumi.dev.yaml` names resources — {% if cookiecutter.deploy_target == "aws-ecs-express" %}it is why the
Express service is `api-dev`{% else %}it is why the
Cloud Run service is `api-dev`{% endif %} — and the application never sees it.
`{{ cookiecutter.project_slug }}:appEnvironment` {% if cookiecutter.deploy_target == "aws-ecs-express" %}becomes `ENVIRONMENT` on the task and decides{% else %}becomes `ENVIRONMENT` on the revision and decides{% endif %}
how the service behaves; it defaults to `staging`, so a first deploy answers `/docs`. Set it to
`production` in the stack real users reach. [infra/README.md](infra/README.md) has the table.

## Configuration, and where each value lives

Three places, and the distinction is the security boundary. Nothing moves between them.

| Kind | Set in | Read by | Examples |
| --- | --- | --- | --- |
| Local development | `.env`, copied from `.env.example` and never committed | Compose and the app on your machine | `ENVIRONMENT`, `JSON_LOGS`{% if cookiecutter.use_postgres == "yes" %}, `DATABASE_AUTH=password`, `POSTGRES_*`{% endif %} |
| Deploy configuration | GitHub repository **variables** | `.github/workflows/cd.yml` {% if cookiecutter.deploy_target == "aws-ecs-express" %}| `AWS_DEPLOY_ROLE_ARN`, `AWS_PREVIEW_ROLE_ARN`, `AWS_REGION`, `IMAGE_REPO`, `ECS_CLUSTER`, `ECS_SERVICE_ARN`, `SERVICE_URL`{% if cookiecutter.use_postgres == "yes" %}, `ECS_MIGRATE_TASK_FAMILY`{% endif %} |{% else %}| `WIF_PROVIDER`, `DEPLOY_SA`, `GCP_REGION`, `IMAGE_REPO`, `CLOUD_RUN_SERVICE`, `CLOUD_RUN_MIGRATE_JOB` |{% endif %}
{% if cookiecutter.deploy_target == "aws-ecs-express" %}| Runtime secrets | Secrets Manager, pinned by version | the ECS task, as an environment variable |{% else %}| Runtime secrets | Secret Manager, pinned by version | the Cloud Run revision, as an environment variable |{% endif %} {% if cookiecutter.use_postgres == "yes" %}`JWT_SECRET`{% endif %}{% if cookiecutter.use_postgres == "yes" and cookiecutter.use_sentry == "yes" %}, {% endif %}{% if cookiecutter.use_sentry == "yes" %}`SENTRY_DSN`{% endif %}{% if cookiecutter.use_postgres == "yes" and cookiecutter.deploy_target == "gcp-cloud-run" %}, and the migration job's `DATABASE_PASSWORD`{% endif %} |

{% if cookiecutter.use_postgres == "yes" %}**No secret value passes through Pulumi** — not the program, not `Pulumi.<stack>.yaml`, not
state (Pulumi standards MUST-3, ADR-0023). The stack creates empty containers; `just infra-secrets`
generates {% if cookiecutter.deploy_target == "aws-ecs-express" %}`JWT_SECRET`{% else %}`JWT_SECRET` and the `postgres` password{% endif %}, adds each as a new version without printing it,
and pins the version number in stack configuration. `JWT_SECRET` is mounted on the service and on
nothing else: {% if cookiecutter.deploy_target == "aws-ecs-express" %}the migration task is{% else %}the migration job is{% endif %} deliberately not granted the signing key; it signs nothing.

**The service has no database password at all.** `DATABASE_AUTH` decides how it logs in:
`password` locally, in tests and for the migration, {% if cookiecutter.deploy_target == "aws-ecs-express" %}`rds-iam` deployed — a token signed with the task
role for every new connection. The migration task logs in as the RDS master user, whose password
RDS generates, stores and rotates in a secret of its own; only the migration task's execution role
can read it.{% else %}`cloud-sql-iam` deployed — the Cloud SQL connector, as
the revision's service account. The migration job logs in as `postgres`, with the password
`just infra-secrets` set, which only the migration job's own service account can read.{% endif %}
Every migration ends by granting the service's role the tables. `.env.example` has every setting
and their precedence.

A deployed server refuses to start with the sentinel signing key, or with one under 32 characters.
That check is in `app/core/security/tokens.py` and runs from `create_app`, rather than in the
settings validator, so the process that legitimately lacks the key can still boot.{% endif %}{% if cookiecutter.use_sentry == "yes" %}
`SENTRY_DSN` is the one a human fills in, {% if cookiecutter.deploy_target == "aws-ecs-express" %}from a secret the stack creates empty.{% else %}from a container the stack creates empty.{% endif %}{% endif %}

Repository variables are deliberately variables and not secrets: {% if cookiecutter.deploy_target == "aws-ecs-express" %}an account id, a region and a
role ARN grant nothing{% else %}a project id, a region and a
service-account email grant nothing{% endif %} on their own, and being readable in logs is what makes a failed
deploy debuggable. The one repository *secret* is `SENTRY_AUTH_TOKEN`, if you wire up release
markers.

No secret value belongs in a file in this repository. Not in code, not in a test fixture, not in a
Pulumi program, not in CI YAML, not in a tracked `.env`. `gitleaks` enforces that on every push and
in a pre-commit hook, reading `.gitleaks.toml` at the root. That file extends the default rule set
rather than replacing it, and allowlists two *lines* — Pulumi's `encryptedkey` and
`secretsprovider`, which are KMS ciphertext and a key URL. Adding a path to it is how a scanner
gets quietly switched off; if a finding is a false positive, allowlist the line.

## Related

- [infra/README.md](infra/README.md) — the deployment order and what only a human can do
- `AGENTS.md` — the map for coding agents; `backend/AGENTS.md` for the service itself
- Ask Athena for the rules this repository is built to: backend layering, containerization,
  {% if cookiecutter.deploy_target == "aws-ecs-express" %}GitHub Actions and Pulumi.{% else %}GitHub Actions, Cloud Run and Pulumi.{% endif %} Retrieve them by topic rather than by path — the handbook
  is served against the question you actually have.
