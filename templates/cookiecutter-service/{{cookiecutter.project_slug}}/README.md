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
├── .github/workflows/       # ci.yml, cd.yml — paths-filtered per component
├── .env.example             # stack-wide configuration
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
directory rather than restructuring this one.

## The backend, in one paragraph

Requests flow one way: `View → Facade → Service → Repository → Model → DB`. Views bind input and
call exactly one facade method. Facades own a use case and convert entities to wire schemas.
Services own the business rules and raise `AppError` subclasses — never `HTTPException`, so they
stay callable from a worker or a CLI. Repositories own every query and `flush()`; the session
dependency is the only thing that commits. `app/api/deps.py` is the only place the object graph is
assembled, which makes it the only seam a test has to override. `just lint` enforces all of this
mechanically via `import-linter`.

## Deploying

`.github/workflows/cd.yml` builds, pushes an immutable `:sha` image to Artifact Registry and
updates the Cloud Run service. It is **inert until configured**: every job is gated on the
repository variable `WIF_PROVIDER`. Set these repository variables to turn it on:

| Variable | Example |
| --- | --- |
| `WIF_PROVIDER` | `projects/123/locations/global/workloadIdentityPools/github/providers/github` |
| `DEPLOY_SA` | `deployer@{{ cookiecutter.gcp_project_id }}.iam.gserviceaccount.com` |
| `GCP_REGION` | `{{ cookiecutter.gcp_region }}` |
| `IMAGE_REPO` | `{{ cookiecutter.gcp_region }}-docker.pkg.dev/{{ cookiecutter.gcp_project_id }}/services` |
| `CLOUD_RUN_SERVICE` | `{{ cookiecutter.project_slug }}` |
| `CLOUD_RUN_MIGRATE_JOB` | `{{ cookiecutter.project_slug }}-migrate` (optional) |

Authentication is Workload Identity Federation. A downloaded service-account key is on the
handbook's banned list and must not be introduced here.

## Related

- `AGENTS.md` — the map for coding agents; `backend/AGENTS.md` for the service itself
- The handbook: `docs/rules/backend/`, `docs/rules/delivery/`, `docs/rules/observability/`
