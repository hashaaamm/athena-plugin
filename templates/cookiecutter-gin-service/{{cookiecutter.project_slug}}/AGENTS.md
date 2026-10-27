# Agent rules — {{ cookiecutter.project_name }}

The general standards come from Athena: ask it for what a change needs before planning it — the
athena skill says how. This file is a **map** of the repository; each subtree carries its own
rules.

| Subtree | Rules | What it is |
| --- | --- | --- |
| `backend/` | [backend/AGENTS.md](backend/AGENTS.md) | Gin service in Go, Postgres via pgx + sqlc, {% if cookiecutter.deploy_target == "aws-ecs-express" %}ECS Express Mode{% else %}Cloud Run{% endif %} |
| `infra/` | [infra/README.md](infra/README.md) | Pulumi (Go): the cloud resources and the order they are created in |

## What this service is

{{ cookiecutter.description }}

<<Replace this with one paragraph: what it does, who calls it, what data it owns. An agent that
does not know this will make architecturally wrong choices confidently.>>

## Where things live, and why

- **Root** holds anything that orchestrates more than one component, or that a developer runs from
  the repository root: `justfile`, `docker-compose*.yml`, `.github/workflows/`, `.env.example`.
- **`backend/`** holds everything specific to the Go service: `go.mod`, `migrations/`, `queries/`,
  `sqlc.yaml`, `docker/`, its own `justfile`.
- **`infra/`** holds the Pulumi program, its bootstrap scripts and its own Go module. It describes
  where this repository runs. It is a sibling of `backend/`, not a part of it.

Adding a component means adding a directory and one line to the root `justfile`. It never means
moving what is already here. A browser client is not a component of this repository: it is its
own repository, generated from the handbook's `cookiecutter-react-app` template, and pointing it at
this API is stack configuration (`corsOrigins`) — `infra/README.md` has the steps.

## Commands

Always use `just`, from the repository root. Never invent a raw `docker compose` invocation.

| Task | Command |
| --- | --- |
| Run the stack | `just dev` |
| Stop it, remove volumes | `just down` |
| Full test suite | `just test` |
| One package or test | `just test-one ./internal/service -run TestLogin` |
| Lint, vet, layer rules | `just lint` |
| Regenerate sqlc, Swagger, OpenAPI | `just gen` (after editing `queries/`, `migrations/` or a handler's annotations) |
| Everything CI runs | `just check` |
| Unit-test the Pulumi program | `just infra-test` |
| See what an apply would change | `just infra-preview` |
{%- if cookiecutter.use_postgres == "yes" %}
| Apply migrations | `just db-migrate` |
| New migration | `just db-new create_invoices` |
| Reset local data | `just db-reset` |
{%- endif %}

## Definition of done here

- [ ] `just check` passes
- [ ] A new endpoint has handler + service + repository + queries + tests, and Swagger annotations
- [ ] `just gen` was run in the same commit as the change that motivated it
{%- if cookiecutter.use_postgres == "yes" %}
- [ ] The migration has a working `-- +goose Down`
{%- endif %}
- [ ] No new dependency outside the handbook's approved list
- [ ] No secret in any file

## Do not

- Do not disable a linter, add `//nolint`, or delete a test to get to green. Surface the conflict
  instead.
- Do not edit generated code (`backend/internal/db/`, `backend/docs/`) by hand. Change the SQL or
  the annotation and run `just gen`.
- Do not modify `.github/workflows/` without saying so explicitly in your summary.
- Do not run `{% if cookiecutter.deploy_target == "aws-ecs-express" %}aws{% else %}gcloud{% endif %}` mutations or anything that changes cloud state. That includes
  `just infra-up` and `just infra-sync-github`, and the bootstrap script. `just infra-test` and
  `just infra-preview` are the two that change nothing; everything else under `infra/` needs a
  human who has read the preview.
- Do not run `just infra-destroy` or `just infra-teardown`, ever, and do not suggest removing the
  guards on them. The teardown script deletes the Pulumi state bucket, which is irreversible and
  is the only record of what exists.
- Do not put a secret value in `infra/`. The stack creates empty {% if cookiecutter.deploy_target == "aws-ecs-express" %}secrets{% else %}containers{% endif %}; a person fills them.

## Recurring failures

<<Keep a list of the three things that most often break locally. It is the cheapest runbook there
is, and agents use it immediately.>>
