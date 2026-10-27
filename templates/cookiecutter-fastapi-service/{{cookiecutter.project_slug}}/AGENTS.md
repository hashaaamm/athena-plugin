# Agent rules — {{ cookiecutter.project_name }}

The general standards come from Athena: ask it for what a change needs before planning it — the
athena skill says how. This file is a **map** of the repository; each subtree carries its own
rules.

| Subtree | Rules | What it is |
| --- | --- | --- |
| `backend/` | [backend/AGENTS.md](backend/AGENTS.md) {% if cookiecutter.deploy_target == "aws-ecs-express" %}| FastAPI service, Postgres, ECS Express Mode |{% else %}| FastAPI service, Postgres, Cloud Run |{% endif %}
| `infra/` | [infra/README.md](infra/README.md) | Pulumi (Python): the cloud resources and the order they are created in |

This repository is the API only. A browser client is a separate repository, generated from
`cookiecutter-react-app`; this one serves it once `corsOrigins`{% if cookiecutter.use_postgres == "yes" %} and `refreshTransport`{% endif %} are set in the
stack — the "A browser client" section of `README.md` has the keys and their limits.

## What this service is

{{ cookiecutter.description }}

<<Replace this with one paragraph: what it does, who calls it, what data it owns. An agent that
does not know this will make architecturally wrong choices confidently.>>

## Where things live, and why

- **Root** holds anything that orchestrates more than one component, or that a developer runs from
  the repository root: `justfile`, `docker-compose*.yml`, `.github/workflows/`, `.env.example`.
- **`backend/`** holds everything specific to the Python service: `pyproject.toml`, `alembic/`,
  `docker/`, its own `justfile`.
- **`infra/`** holds the Pulumi stack, its bootstrap script and its own Python toolchain. It
  describes where this repository runs. It is a sibling of `backend/`, not a part of it.

Adding a component means adding a directory and one line to the root `justfile`. It never means
moving what is already here.

## Commands

Always use `just`, from the repository root. Never invent a raw `docker compose` invocation.

| Task | Command |
| --- | --- |
| Run the stack | `just dev` |
| Stop it, remove volumes | `just down` |
| Full test suite | `just test` |
| One test | `just test-one tests/test_health.py::test_ready_reports_dependencies` |
| Lint, types, layer contracts | `just lint` |
| Everything CI runs | `just check` |
| Unit-test the Pulumi stack | `just infra-test` |
| See what an apply would change | `just infra-preview` |
{%- if cookiecutter.use_postgres == "yes" %}
| Apply migrations | `just db-migrate` |
| New migration | `just db-revision "message"` |
| Check the history has one head | `just db-heads` — no database needed; this is CI's gate |
| Reset local data | `just db-reset` |
{%- endif %}

## Definition of done here

- [ ] `just check` passes
- [ ] A new endpoint has router + service + repository + schemas + tests
{%- if cookiecutter.use_postgres == "yes" %}
- [ ] The migration has a working `downgrade()` and the history has one head
{%- endif %}
- [ ] No new dependency outside the handbook's approved list
- [ ] No secret in any file

## Do not

- Do not disable a lint rule, add `# type: ignore`, or delete a test to get to green. Surface the
  conflict instead.
- Do not modify `.github/workflows/` without saying so explicitly in your summary.
{% if cookiecutter.deploy_target == "aws-ecs-express" %}- Do not run `aws` mutations or anything that changes cloud state.{% else %}- Do not run `gcloud` mutations or anything that changes cloud state.{% endif %} That includes
  `just infra-up`{% if cookiecutter.use_postgres == "yes" %}, `just infra-secrets`{% endif %} and `just infra-sync-github`, and the bootstrap script. `just infra-test` and
  `just infra-preview` are the two that change nothing; everything else under `infra/` needs a
  human who has read the preview.
- Do not run `just infra-destroy` or `just infra-teardown`, ever, and do not suggest removing the
  guards on them. The teardown script deletes the Pulumi state bucket, which is irreversible and
  is the only record of what exists; it refuses without a terminal and without the bucket name
  typed back, and that is the point of it.
- Do not put a secret value in `infra/` — not in the program, not in `Pulumi.<stack>.yaml`, not in
  state. {% if cookiecutter.deploy_target == "aws-ecs-express" %}The stack creates empty secrets{% else %}The stack creates empty containers{% endif %}; {% if cookiecutter.use_postgres == "yes" %}`just infra-secrets` fills the generated ones and a
  person fills the rest{% else %}a person fills them{% endif %}, and the stack pins the version number it is given.

## Recurring failures

<<Keep a list of the three things that most often break locally. It is the cheapest runbook there
is, and agents use it immediately.>>
