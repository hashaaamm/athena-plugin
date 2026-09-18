# Agent rules — {{ cookiecutter.project_name }}

Read the organisation-wide rules first: the Engineering Athena handbook's `AGENTS.md`. They apply
in full. This file is a **map** of the repository; each subtree carries its own rules.

| Subtree | Rules | What it is |
| --- | --- | --- |
| `backend/` | [backend/AGENTS.md](backend/AGENTS.md) | FastAPI service, Postgres, Cloud Run |
{%- if cookiecutter.include_frontend == "yes" %}
| `frontend/` | [frontend/AGENTS.md](frontend/AGENTS.md) | Web client (placeholder — not yet built) |
{%- endif %}

## What this service is

{{ cookiecutter.description }}

<<Replace this with one paragraph: what it does, who calls it, what data it owns. An agent that
does not know this will make architecturally wrong choices confidently.>>

## Where things live, and why

- **Root** holds anything that orchestrates more than one component, or that a developer runs from
  the repository root: `justfile`, `docker-compose*.yml`, `.github/workflows/`, `.env.example`.
- **`backend/`** holds everything specific to the Python service: `pyproject.toml`, `alembic/`,
  `docker/`, its own `justfile`.

Adding a component means adding a directory and one line to the root `justfile`. It never means
moving what is already here.

## Commands

Always use `just`, from the repository root. Never invent a raw `docker compose` invocation.

| Task | Command |
| --- | --- |
| Run the stack | `just dev` |
| Stop it, remove volumes | `just down` |
| Full test suite | `just test` |
| One test | `just test-one tests/test_item_api.py::test_create_and_read_back` |
| Lint, types, layer contracts | `just lint` |
| Everything CI runs | `just check` |
{%- if cookiecutter.use_postgres == "yes" %}
| Apply migrations | `just db-migrate` |
| New migration | `just db-revision "message"` |
| Reset local data | `just db-reset` |
{%- endif %}

## Definition of done here

- [ ] `just check` passes
- [ ] A new endpoint has router + facade + service + repository + schemas + tests
{%- if cookiecutter.use_postgres == "yes" %}
- [ ] The migration has a working `downgrade()` and the history has one head
{%- endif %}
- [ ] No new dependency outside the handbook's approved list
- [ ] No secret in any file

## Do not

- Do not disable a lint rule, add `# type: ignore`, or delete a test to get to green. Surface the
  conflict instead.
- Do not modify `.github/workflows/` without saying so explicitly in your summary.
- Do not run `gcloud` mutations or anything that changes cloud state.

## Recurring failures

<<Keep a list of the three things that most often break locally. It is the cheapest runbook there
is, and agents use it immediately.>>
