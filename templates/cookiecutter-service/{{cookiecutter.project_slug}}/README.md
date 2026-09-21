# {{ cookiecutter.project_name }}

{{ cookiecutter.description }}

Generated from the Engineering Athena `cookiecutter-service` template. The handbook is the
authority on every convention below; this file only says where things are.

## Run it

```bash
cp .env.example .env                    # `just dev` does this for you if you forget
(cd backend && uv lock)                 # once: resolve the lockfile the images build from
{%- if cookiecutter.include_frontend == "yes" %}
(cd frontend && just lock)              # once: the same, for pnpm-lock.yaml
{%- endif %}
just dev                                # API on http://localhost:8000/docs{% if cookiecutter.include_frontend == "yes" %}, app on http://localhost:3000{% endif %}
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
├── .github/workflows/       # ci.yml, cd.yml, infra.yml{% if cookiecutter.include_frontend == "yes" %}, frontend-ci.yml, frontend-cd.yml{% endif %} — paths-filtered per component
├── .env.example             # stack-wide configuration
├── .gitleaks.toml           # secret-scan config: default rules, plus two allowlisted lines
├── infra/                   # Pulumi (Python): the cloud resources — see infra/README.md
│   ├── bootstrap/           # plain gcloud; creates the Pulumi state bucket, and removes it again
│   ├── scripts/             # copies stack outputs into GitHub repository variables
│   └── pulumi/              # the stack, its components and their unit tests
├── backend/                 # the FastAPI service
│   ├── app/{api,services,repositories,models,schemas,core}
│   ├── alembic/
│   ├── docker/              # the dev and production images, and the server's start script
│   ├── tests/
│   ├── justfile             # backend-only recipes
│   └── pyproject.toml
{%- if cookiecutter.include_frontend == "yes" %}
└── frontend/                # the React SPA (one page: the health dashboard) — see frontend/README.md
    ├── src/{components,lib/api,routes}
    ├── docker/              # the production image: build with pnpm, serve with nginx
    ├── justfile             # frontend-only recipes
    └── package.json
{%- endif %}
```

**Root or `backend/`?** Anything that orchestrates more than one component, or that a developer
runs from the repository root, lives at the root. Anything specific to the Python service lives in
`backend/`. That is the whole rule, and it is why adding a frontend later is dropping in a
directory rather than restructuring this one. `infra/` is a sibling of `backend/` for the same
reason: it describes where the whole repository runs, not how the Python service is built, and it
has its own toolchain and its own workflow.

## The backend, in one paragraph

{% if cookiecutter.use_postgres == "yes" %}It answers `/health/live`, `/health/ready`, and four authentication endpoints:
`POST /api/v1/auth/register`, `POST /api/v1/auth/login`, `GET /api/v1/auth/me` and
`POST /api/v1/auth/change-password`. Those four are a real feature and the worked example of the
layering at the same time — router, service, repository, model, in five files you can read in ten
minutes. What they are not is complete: there are no refresh tokens, no logout, no revocation and
no roles. `backend/AGENTS.md` says what that costs you and where to read next.{% else %}The endpoints it answers today are `/health/live` and `/health/ready`, and nothing else. With no
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

{% if cookiecutter.include_frontend == "yes" %}## The frontend, in one paragraph

A React 19 SPA in TypeScript, built with Vite: TanStack Router for routes, TanStack Query for
every read and write, Tailwind v4 with shadcn/ui for the styling, react-hook-form and zod for
forms, Vitest and Testing Library for the suite. The API client in `src/lib/api/` is **generated
from the backend's own OpenAPI document** by `just gen-api` — so a renamed field is a failed
build here rather than `undefined` in front of a user. Run it in the same change as the backend
edit that motivated it. The production image builds the static bundle and serves it with nginx;
`VITE_*` values are inlined at build time, which makes them build args and means none of them
can be a secret. [frontend/README.md](frontend/README.md) has the rest.
{% endif %}
## Deploying

`infra/` declares the cloud resources in Pulumi. `.github/workflows/cd.yml` builds an immutable
`:sha` image and updates one field on the backend's Cloud Run service — the image tag{% if cookiecutter.include_frontend == "yes" %};
`.github/workflows/frontend-cd.yml` does the same for the SPA's nginx image{% endif %}. Both are
**inert until configured**: every job is gated on the repository variable `WIF_PROVIDER`, which the
stack exports and `just infra-sync-github` sets.

**[infra/README.md](infra/README.md) is the order it has to happen in**, and it draws the line
between what this repository ships ready to run and what only you can do — a GCP project with
billing, reading the preview before the first apply, populating the secret containers, a domain,
and branch protection. Everything on the other side of that line is generated, wired and tested
here; if you find yourself writing deploy plumbing by hand, that is a bug in the template.
{% if cookiecutter.include_frontend == "yes" %}
One consequence is worth knowing before the first deploy: **the API's address is compiled into the
frontend bundle.** Vite inlines `VITE_*` at build time, so `frontend-cd.yml` passes the backend's
URL as a Docker build argument and refuses to build when it does not have one. Changing where the
API answers means re-running `just infra-sync-github` and then Frontend CD — not editing a variable
on a running revision, which would change nothing at all.
{% endif %}
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
the bucket first strands the state that `destroy` needs — makes you type the bucket name, and prints
the KMS commands you cannot run for another 30 days, because a KMS key is scheduled for destruction
rather than deleted. [infra/README.md](infra/README.md) has the order and the reasons.
{%- if cookiecutter.use_postgres == "yes" %}

It is worth knowing before the first apply that **Cloud SQL is the only thing here that bills while
idle**, and that its settings are already the cheapest ones that work — shared-core, the minimum
10 GB of HDD, zonal, no point-in-time recovery outside production. `infra/README.md` says what each
one is, what the floor costs you (no SLA), and what the only two levers below it give up.
{%- endif %}

**Two things here are called "environment" and they are not the same one.**
`{{ cookiecutter.project_slug }}:environment` in `Pulumi.dev.yaml` names resources — it is why the
Cloud Run service is `api-dev` — and the application never sees it.
`{{ cookiecutter.project_slug }}:appEnvironment` becomes `ENVIRONMENT` on the revision and decides
how the service behaves; it defaults to `staging`, so a first deploy answers `/docs`. Set it to
`production` in the stack real users reach. [infra/README.md](infra/README.md) has the table.

## Configuration, and where each value lives

Three places, and the distinction is the security boundary. Nothing moves between them.

| Kind | Set in | Read by | Examples |
| --- | --- | --- | --- |
| Local development | `.env`, copied from `.env.example` and never committed | Compose and the app on your machine | `ENVIRONMENT`, `JSON_LOGS`{% if cookiecutter.use_postgres == "yes" %}, `POSTGRES_*`{% endif %} |
| Deploy configuration | GitHub repository **variables** | `.github/workflows/cd.yml`{% if cookiecutter.include_frontend == "yes" %} and `frontend-cd.yml`{% endif %} | `WIF_PROVIDER`, `DEPLOY_SA`, `GCP_REGION`, `IMAGE_REPO`, `CLOUD_RUN_SERVICE`, `CLOUD_RUN_MIGRATE_JOB`{% if cookiecutter.include_frontend == "yes" %}, `CLOUD_RUN_FRONTEND_SERVICE`, `SERVICE_URL`{% endif %} |
| Runtime secrets | Secret Manager | the Cloud Run revision, as an environment variable | {% if cookiecutter.use_postgres == "yes" %}`DATABASE_URL_OVERRIDE`, `JWT_SECRET`{% endif %}{% if cookiecutter.use_postgres == "yes" and cookiecutter.use_sentry == "yes" %}, {% endif %}{% if cookiecutter.use_sentry == "yes" %}`SENTRY_DSN`{% endif %} |

{% if cookiecutter.use_postgres == "yes" %}The stack generates the first two and nobody ever types either: the database DSN, and `JWT_SECRET`
— 48 random characters minted by Pulumi, mounted on the service and on nothing else. The migration
job is deliberately not granted the signing key; it signs nothing.

A deployed server refuses to start with the sentinel signing key, or with one under 32 characters.
That check is in `app/core/security/tokens.py` and runs from `create_app`, rather than in the
settings validator, so the process that legitimately lacks the key can still boot.{% endif %}{% if cookiecutter.use_sentry == "yes" %}
`SENTRY_DSN` is the one a human fills in, from a container the stack creates empty.{% endif %}

Repository variables are deliberately variables and not secrets: a project id, a region and a
service-account email grant nothing on their own, and being readable in logs is what makes a failed
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
  GitHub Actions, Cloud Run and Pulumi. Retrieve them by topic rather than by path — the handbook
  is served against the question you actually have.
