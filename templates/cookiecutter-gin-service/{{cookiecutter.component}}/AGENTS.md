# Agent rules — {{ cookiecutter.project_name }}

The general standards come from Athena: ask it for what a change needs before planning it — the
athena skill says how. This file is a **map** of the `{{ cookiecutter.component }}/` component; each subtree carries
its own rules. The repository's root `AGENTS.md` lists every component.

| Subtree | Rules | What it is |
| --- | --- | --- |
| `backend/` | [backend/AGENTS.md](backend/AGENTS.md) | Gin service in Go, Postgres via pgx + sqlc, {% if cookiecutter.deploy_target == "aws-ecs-express" %}ECS Express Mode{% else %}Cloud Run{% endif %} |
| `infra/` | [infra/README.md](infra/README.md) | Pulumi (Go): the cloud resources and the order they are created in |

## What this service is

{{ cookiecutter.description }}

<<Replace this with one paragraph: what it does, who calls it, what data it owns. An agent that
does not know this will make architecturally wrong choices confidently.>>

## Where things live, and why

This service is one **component** of its repository: the folder `{{ cookiecutter.component }}/`, generated from the
handbook's `cookiecutter-gin-service` template (ADR-0024). Everything it owns lives in this folder,
and everything it creates outside it carries its name.

- **`{{ cookiecutter.component }}/`** holds anything that spans the component's parts: `justfile`,
  `docker-compose*.yml`, `.env.example`, `.gitleaks.toml`.
- **`backend/`** holds everything specific to the Go service: `go.mod`, `migrations/`, `queries/`,
  `sqlc.yaml`, `docker/`, its own `justfile`.
- **`infra/`** holds the Pulumi program, its bootstrap scripts and its own Go module. It describes
  where this component runs. It is a sibling of `backend/`, not a part of it.
- **The repository root** holds only what GitHub and `just` look for there:
  `.github/workflows/{{ cookiecutter.component }}-ci.yml`, `{{ cookiecutter.component }}-cd.yml` and `{{ cookiecutter.component }}-infra.yml`, a `justfile` with a
  `mod {{ cookiecutter.component }}` line, and an `AGENTS.md` listing the components. Every repository variable this
  component's pipelines read is prefixed `{{ cookiecutter.__component_var }}_`.

Another component — a browser client generated from `cookiecutter-react-app`, a second service —
is another folder beside this one, generated the same way, never a change to what is here. Pointing
a browser client at this API is stack configuration (`corsOrigins`): `infra/README.md` has the
steps.

## Commands

Always use `just` — from this folder as below, or from the repository root as
`just {{ cookiecutter.component }} <recipe>`. Never invent a raw `docker compose` invocation.

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
- Do not modify `.github/workflows/{{ cookiecutter.component }}-*.yml` without saying so explicitly in your summary,
  and keep every repository variable they read prefixed `{{ cookiecutter.__component_var }}_`.
- Do not run `{% if cookiecutter.deploy_target == "aws-ecs-express" %}aws{% else %}gcloud{% endif %}` mutations or anything that changes cloud state. That includes
  `just infra-up`, {% if cookiecutter.use_postgres == "yes" %}`just infra-secrets`, {% endif %}`just infra-first-deploy`, `just infra-sync-github`, and the bootstrap script. `just infra-test` and
  `just infra-preview` are the two that change nothing; everything else under `infra/` needs a
  human who has read the preview.
- Do not run `just infra-destroy` or `just infra-teardown`, ever, and do not suggest removing the
  guards on them. The teardown script deletes the Pulumi state bucket, which is irreversible and
  is the only record of what exists.
- Do not put a secret value in `infra/` — not in the program, not in `Pulumi.<stack>.yaml`, not in
  state, and not as a generated value either (ADR-0023). The stack creates empty {% if cookiecutter.deploy_target == "aws-ecs-express" %}secrets{% else %}containers{% endif %}{% if cookiecutter.use_postgres == "yes" %};
  `just infra-secrets` or a person fills them, and only the version goes into stack configuration{% else %}; a
  person fills them, and only the version goes into stack configuration{% endif %}.
{%- if cookiecutter.use_postgres == "yes" %}
- Do not give the deployed service a database password. It authenticates with
  `DATABASE_AUTH={% if cookiecutter.deploy_target == "aws-ecs-express" %}rds-iam{% else %}cloud-sql-iam{% endif %}` as its own cloud identity and holds row privileges only; schema changes
  belong to the migration {% if cookiecutter.deploy_target == "aws-ecs-express" %}task{% else %}job{% endif %}, which grants the service's role after every run.
{%- endif %}

## Recurring failures

<<Keep a list of the three things that most often break locally. It is the cheapest runbook there
is, and agents use it immediately.>>
