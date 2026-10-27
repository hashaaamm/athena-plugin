"""Post-generation clean-up, and the component's place in the repository around it.

Cookiecutter renders the whole tree into <repo-root>/<component>/ and then runs this, with that
folder as the working directory. Deleting here rather than branching in Jinja keeps the templates
readable: a file either exists for every project or it is removed once, in one place, by a rule you
can read.

The component model (ADR-0024): a repository holds one folder per generated template, and each
component's app and infra/ live together in its folder. Three things cannot live there, because
GitHub and `just` look for them at the repository root — the workflows, the root justfile and the
root AGENTS.md — so this hook puts them there, prefixed with the component's name, and refuses
rather than overwrite one that another component already owns.
"""

from __future__ import annotations

import shutil
import stat
import sys
from pathlib import Path

PROJECT = Path.cwd()
ROOT = PROJECT.parent

COMPONENT = "{{ cookiecutter.component }}"
COMPONENT_VAR = "{{ cookiecutter.__component_var }}"

#: The only templating in a workflow. Workflows are copied without Jinja (`_copy_without_render`),
#: because a GitHub Actions expression and a Jinja one are written with the same double braces;
#: these two tokens are what makes one workflow file work for any component name.
TOKENS = {
    "__COMPONENT_VAR__": COMPONENT_VAR,
    "__COMPONENT__": COMPONENT,
}

#: Scripts a human or a pipeline runs directly. Cookiecutter writes rendered files with the default
#: mode, so the executable bit has to be set here or the first command of the deployment sequence
#: fails with "permission denied".
EXECUTABLE = (
    "infra/bootstrap/state-bucket.sh",
    "infra/bootstrap/teardown.sh",
    "infra/scripts/secrets.sh",
    "infra/scripts/sync-github.sh",
    "infra/scripts/first-deploy.sh",
)

USE_POSTGRES = "{{ cookiecutter.use_postgres }}" == "yes"
USE_SENTRY = "{{ cookiecutter.use_sentry }}" == "yes"
PROJECT_NAME = "{{ cookiecutter.project_name }}"

DEPLOY_TARGET = "{{ cookiecutter.deploy_target }}"


# --- the component model: identical in every template ------------------------------------------


def _workflow_sources() -> list[Path]:
    """Every workflow this component ships: the shared ones and the chosen target's."""
    sources = [*(PROJECT / ".github" / "workflows").glob("*.yml")]
    sources += [*(PROJECT / "deploy" / DEPLOY_TARGET / "workflows").glob("*.yml")]
    return sorted(sources, key=lambda path: path.name)


def _workflow_destination(source: Path) -> Path:
    return ROOT / ".github" / "workflows" / f"{COMPONENT}-{source.name}"


def _refuse_to_overwrite_workflows() -> None:
    """Stop before writing anything if another component already owns one of these files.

    pre_gen_project.py refuses the same thing first, because `cookiecutter -f` overwrites an
    existing component folder before this hook runs; this is the check for exactly the workflows
    the chosen deploy target ships. Two components with one name would share every workflow, every
    repository variable and every cloud resource name, and the second would silently take the
    first one's place.
    """
    taken = [_workflow_destination(source) for source in _workflow_sources()]
    taken = [path for path in taken if path.exists()]
    if taken:
        names = "\n".join(f"    {path.relative_to(ROOT)}" for path in taken)
        sys.stderr.write(
            f"\n  ERROR: component {COMPONENT!r} is already in this repository:\n\n{names}\n\n"
            "  Nothing was written. Generate it again with a different name, e.g.\n"
            f"  component={COMPONENT}2, or remove those files first if they are left over.\n\n"
        )
        sys.exit(1)


def _install_workflows() -> None:
    """Write each workflow to <root>/.github/workflows/<component>-<file>, tokens substituted."""
    for source in _workflow_sources():
        text = source.read_text()
        for token, value in TOKENS.items():
            text = text.replace(token, value)
        destination = _workflow_destination(source)
        destination.parent.mkdir(parents=True, exist_ok=True)
        destination.write_text(text)
    shutil.rmtree(PROJECT / ".github")


#: What a root `justfile` starts as when this is the repository's first component. Identical in every
#: template, because whichever component is generated first writes it.
ROOT_JUSTFILE = """\
# The repository root. Each component is a folder with a justfile of its own, mounted here as a
# `just` module (just 1.31 or later): `just <component> <recipe>` from here, or `just <recipe>` from
# inside the folder. A module's recipes run in its own folder either way.
# Ask Athena for the local development and command runner rules.

# Show every component and its recipes
default:
    @just --list --list-submodules
"""

#: What a root `AGENTS.md` starts as when this is the repository's first component. Identical in
#: every template, for the same reason.
ROOT_AGENTS = """\
# Agent rules

The general standards come from Athena: ask it for what a change needs before planning it — the
athena skill says how. This file is a **map**: the repository is made of components, a folder
each, and every component carries the rules for working inside it in its own `AGENTS.md`. Read that
one before changing anything in its folder.

Commands go through `just`: `just <component> <recipe>` from here, or `just <recipe>` from inside
the component's folder. A component's workflows are `.github/workflows/<component>-*.yml`, and the
repository variables it reads are prefixed with its name in capitals.

## Components

"""


def _root_justfile() -> None:
    """Mount the component as a module of the root justfile, creating that file if needed.

    `just` accepts `justfile` or `.justfile`, in any case, and refuses to choose between two — so an
    existing one under any of those names is the one appended to.
    """
    names = ("justfile", ".justfile")
    existing = [path for path in ROOT.iterdir() if path.is_file() and path.name.lower() in names]
    path = existing[0] if existing else ROOT / "justfile"
    if not path.exists():
        path.write_text(ROOT_JUSTFILE)
    text = path.read_text()
    line = f"mod {COMPONENT}"
    entries = [entry.strip() for entry in text.splitlines()]
    if line not in entries:
        if text and not text.endswith("\n"):
            text += "\n"
        # A blank line before the first `mod`, so the comment above it is not read as the module's
        # doc comment; the `mod` lines after it follow on.
        separator = "" if not text or any(e.startswith("mod ") for e in entries) else "\n"
        path.write_text(f"{text}{separator}{line}\n")


def _root_agents(summary: str) -> None:
    """List the component in the root `AGENTS.md`, creating that file if needed."""
    path = ROOT / "AGENTS.md"
    if not path.exists():
        path.write_text(ROOT_AGENTS)
    text = path.read_text()
    if f"]({COMPONENT}/AGENTS.md)" not in text:
        if text and not text.endswith("\n"):
            text += "\n"
        path.write_text(f"{text}- [`{COMPONENT}/`]({COMPONENT}/AGENTS.md) — {summary}\n")


# --- this template ----------------------------------------------------------------------------


def _select_deploy_target() -> None:
    """Move the chosen target's stack into place, and drop every other target.

    Each target under deploy/ is a complete implementation of the same interface: an infra/
    directory whose justfile has the same recipes (lock, test, lint, preview, up, secrets,
    first-deploy, sync-github, destroy, teardown), and the deploy workflows those recipes un-gate.
    The component has exactly one of them, at the path every other file already refers to — infra/
    — so nothing outside deploy/ needs to know which target was chosen. Its workflows have already
    gone to the repository root, with the shared ones.
    """
    deploy = PROJECT / "deploy"
    chosen = deploy / DEPLOY_TARGET
    if not chosen.is_dir():
        raise SystemExit(f"unknown deploy_target {DEPLOY_TARGET!r}: no deploy/{DEPLOY_TARGET}/")
    shutil.move(chosen / "infra", PROJECT / "infra")
    shutil.rmtree(deploy)


#: No database means no migrations, no queries, no generated data layer — and no users, so no
#: authentication either. Half an auth system is worse than none: a service with no user table
#: cannot be closed by adding a middleware. Turning `use_postgres` back on is a regeneration.
BACKEND_WITHOUT_POSTGRES: tuple[str, ...] = (
    "backend/migrations",
    "backend/queries",
    "backend/sqlc.yaml",
    "backend/internal/db",
    "backend/internal/repository",
    "backend/internal/security",
    "backend/internal/testdb",
    "backend/internal/service/auth.go",
    "backend/internal/service/auth_test.go",
    "backend/internal/service/main_test.go",
    "backend/internal/http/handler/auth.go",
    "backend/internal/http/handler/auth_body.go",
    "backend/internal/http/handler/auth_cookie.go",
    "backend/internal/http/middleware/actor.go",
    "backend/internal/http/middleware/csrf.go",
    "backend/internal/app/migrate.go",
    "backend/internal/app/database.go",
    "backend/internal/app/database_test.go",
    "backend/internal/app/export_test.go",
    "backend/internal/app/main_test.go",
    "backend/internal/app/auth_api_test.go",
    "backend/internal/app/sessions_api_test.go",
    "backend/internal/app/refresh_cookie_test.go",
    "backend/internal/app/commit_test.go",
    "backend/internal/app/signing_key_test.go",
    "backend/docs/openapi-cookie.json",
)

#: The SDK and its wiring. Error handling still reports to the log; it just has nowhere else to go.
BACKEND_WITHOUT_SENTRY: tuple[str, ...] = ("backend/internal/observability",)

# Keyed by deploy target, because each target is its own stack with its own file list. The paths
# are the generated ones: _select_deploy_target() has already moved the chosen target to infra/.

#: Nothing to provision a database for. The rest of the stack is unchanged: a service, a registry,
#: the identities and the secrets it reads. The shared files — main.go, the config, the API list,
#: the runtime roles — branch in Jinja instead, because they exist either way. On AWS the
#: migration task definition goes too: with no database there is nothing for it to migrate.
#:
#: secrets.sh goes with the database on both targets, because everything it generates — the
#: token signing key, and on GCP the database admin password — exists only with one: no database
#: means no users, so no authentication and nothing to sign.
INFRA_WITHOUT_POSTGRES: dict[str, tuple[str, ...]] = {
    "gcp-cloud-run": (
        "infra/pulumi/internal/components/database.go",
        "infra/pulumi/internal/components/database_test.go",
        "infra/pulumi/main_postgres_test.go",
        "infra/scripts/secrets.sh",
    ),
    "aws-ecs-express": (
        "infra/pulumi/internal/components/database.go",
        "infra/pulumi/internal/components/database_test.go",
        "infra/pulumi/internal/components/migrations.go",
        "infra/pulumi/main_postgres_test.go",
        "infra/scripts/secrets.sh",
    ),
}

#: The stack keeps no Sentry resource of its own beyond one empty secret, which the Jinja in
#: secrets.go already leaves out; only the tests about it go.
INFRA_WITHOUT_SENTRY: dict[str, tuple[str, ...]] = {
    "gcp-cloud-run": (
        "infra/pulumi/internal/components/sentry_test.go",
        "infra/pulumi/main_sentry_test.go",
    ),
    "aws-ecs-express": (
        "infra/pulumi/internal/components/sentry_test.go",
        "infra/pulumi/main_sentry_test.go",
    ),
}


def _remove(*relative_paths: str) -> None:
    for relative in relative_paths:
        target = PROJECT / relative
        if target.is_dir():
            shutil.rmtree(target)
        elif target.exists():
            target.unlink()


def _first_run_message() -> str:
    """What to run next, and the deploy order every infra README gives (ADR-0024)."""
    if DEPLOY_TARGET == "aws-ecs-express":
        subject = ("pulumi config set --plaintext githubSubjectPrefix …", "the OIDC subject GitHub issues")
        secrets = "the signing key"
    else:
        subject = ("(nothing on GCP)", "Workload Identity admits the repository by name")
        secrets = "the signing key and the admin password"
    steps = [
        (f"{COMPONENT}/infra/bootstrap/state-bucket.sh", "the state bucket and its key"),
        (f"pulumi -C {COMPONENT}/infra/pulumi stack init dev …", "the command the bootstrap prints"),
        subject,
        (f"just {COMPONENT} infra-up", "everything except the service"),
        (f"just {COMPONENT} infra-secrets", secrets) if USE_POSTGRES else ("(nothing to generate)", "no database"),
        (f"just {COMPONENT} infra-first-deploy", "the real image, the migrations, the service on it"),
        (f"just {COMPONENT} infra-sync-github", "the stack's outputs into repository variables"),
        ("protect the `infrastructure` environment", "required reviewers, main only — GitHub settings"),
        ("git push", f"CD owns every image, {COMPONENT}-infra.yml applies every infra change"),
    ]
    width = max(len(command) for command, _ in steps)
    order = "".join(
        f"    {number}. {command.ljust(width)}   {note}\n" for number, (command, note) in enumerate(steps, 1)
    )
    return (
        "\n"
        f"  Created {COMPONENT}/ and .github/workflows/{COMPONENT}-*.yml, and registered\n"
        f"  `mod {COMPONENT}` in the root justfile and the component in the root AGENTS.md.\n"
        "\n"
        "  First commands, from the repository root:\n"
        "\n"
        f"    cp {COMPONENT}/.env.example {COMPONENT}/.env\n"
        f"    (cd {COMPONENT}/backend && just lock)   # go.sum is not templated; resolve it once\n"
        f"    (cd {COMPONENT}/infra && just lock)     # the same, for the Pulumi program\n"
        f"    just {COMPONENT} dev\n"
        "\n"
        f"  Then: just {COMPONENT} check   (or: cd {COMPONENT} && just check)\n"
        "\n"
        "  Deploying is a separate sequence, and it starts on your machine, not in CI. Read\n"
        f"  {COMPONENT}/infra/README.md first: the first command there creates cloud resources and\n"
        "  costs money. The order:\n"
        "\n"
        f"{order}"
        "\n"
    )


def main() -> None:
    # First, before anything is written outside this folder: a refusal leaves the repository as
    # it was, and cookiecutter removes the half-made component folder itself.
    _refuse_to_overwrite_workflows()

    _install_workflows()
    _select_deploy_target()

    if not USE_POSTGRES:
        _remove(*BACKEND_WITHOUT_POSTGRES, *INFRA_WITHOUT_POSTGRES[DEPLOY_TARGET])
    if not USE_SENTRY:
        _remove(*BACKEND_WITHOUT_SENTRY, *INFRA_WITHOUT_SENTRY[DEPLOY_TARGET])

    _root_justfile()
    target = "ECS Express Mode" if DEPLOY_TARGET == "aws-ecs-express" else "Cloud Run"
    _root_agents(f"{PROJECT_NAME}: a Gin service in Go, deployed to {target}")

    for relative in EXECUTABLE:
        target_path = PROJECT / relative
        if target_path.exists():
            target_path.chmod(target_path.stat().st_mode | stat.S_IXUSR | stat.S_IXGRP | stat.S_IXOTH)

    sys.stdout.write(_first_run_message())


main()
