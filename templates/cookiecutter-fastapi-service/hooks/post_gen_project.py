"""Post-generation clean-up, and the component's place in the repository around it.

Cookiecutter renders the whole tree into `<repo-root>/<component>/` and then runs this, with that
folder as the working directory. Deleting here rather than branching in Jinja keeps the templates
readable: a file either exists for every project or it is removed once, in one place, by a rule you
can read.

Three things land outside the folder, and they are the only three: the component's workflows, under
the repository's one `.github/workflows/`, prefixed with the component's name; a `mod` line in the
root `justfile`; and a line in the root `AGENTS.md`. Each is written so a second component generated
into the same repository adds to it rather than colliding with it, and so the same component
generated twice is refused before anything is overwritten.
"""

from __future__ import annotations

import shutil
import stat
import sys
from pathlib import Path

#: `<repo-root>/<component>/`, where cookiecutter rendered the tree.
PROJECT = Path.cwd()
#: The repository the component was generated into: `-o` on the cookiecutter command line.
ROOT = PROJECT.parent

COMPONENT = "{{ cookiecutter.component }}"
COMPONENT_VAR = "{{ cookiecutter.__component_var }}"
PROJECT_NAME = "{{ cookiecutter.project_name }}"

#: The only templating in a workflow file. Workflows are copied without Jinja rendering, because a
#: GitHub Actions expression and a Jinja one are written with the same two braces; these two tokens
#: are substituted as plain text instead. `__COMPONENT_VAR__` first: it is the longer token, and a
#: repository variable is written `vars.__COMPONENT_VAR___AWS_REGION`.
TOKENS = (
    ("__COMPONENT_VAR__", COMPONENT_VAR),
    ("__COMPONENT__", COMPONENT),
)

#: Scripts a human or a container runs directly. Cookiecutter writes rendered files with the
#: default mode, so the executable bit has to be set here or `./infra/bootstrap/state-bucket.sh`
#: fails with "permission denied" on the first command of the whole deployment sequence.
#:
#: `backend/docker/start.sh` is the production container's start command. The image copies it with
#: `--chmod=0755`, so a build does not depend on this list; the bit is set here so the same script
#: is runnable by hand from a checkout, which is half the point of it being a file.
EXECUTABLE = (
    "backend/docker/start.sh",
    "infra/bootstrap/state-bucket.sh",
    "infra/bootstrap/teardown.sh",
    "infra/scripts/first-deploy.sh",
    "infra/scripts/sync-github.sh",
    "infra/scripts/secrets.sh",
)

USE_POSTGRES = "{{ cookiecutter.use_postgres }}" == "yes"
USE_SENTRY = "{{ cookiecutter.use_sentry }}" == "yes"

DEPLOY_TARGET = "{{ cookiecutter.deploy_target }}"
#: How the root AGENTS.md names each deploy target.
TARGET_NAMES = {"aws-ecs-express": "ECS Express Mode", "gcp-cloud-run": "Cloud Run", "aws-s3-cloudfront": "S3 and CloudFront"}

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


def _select_deploy_target() -> None:
    """Move the chosen target's stack and pipelines into place, and drop every other target.

    Each target under deploy/ is a complete implementation of the same interface: an infra/
    directory driven by the same `just infra-*` recipes, and the deploy workflows those recipes
    un-gate. The component has exactly one of them, at the paths every other file already refers
    to — infra/ and .github/workflows/ — so nothing outside deploy/ needs to know which target was
    chosen.
    """
    deploy = PROJECT / "deploy"
    chosen = deploy / DEPLOY_TARGET
    if not chosen.is_dir():
        raise SystemExit(f"unknown deploy_target {DEPLOY_TARGET!r}: no deploy/{DEPLOY_TARGET}/")
    shutil.move(chosen / "infra", PROJECT / "infra")
    workflows = PROJECT / ".github" / "workflows"
    for workflow in sorted((chosen / "workflows").iterdir()):
        shutil.move(workflow, workflows / workflow.name)
    shutil.rmtree(deploy)


def _install_workflows() -> None:
    """Copy the component's workflows to the repository's `.github/workflows/`, prefixed.

    GitHub reads workflows from the repository root and nowhere else, so this is the one part of a
    component that cannot live in its folder. Each file becomes `<component>-<name>`, with the two
    tokens substituted. Nothing is overwritten: a destination that exists means this component
    name is taken, and the whole generation is refused before a single file is written, because a
    half-replaced pipeline deploys one component with another's configuration.
    """
    source = PROJECT / ".github" / "workflows"
    target = ROOT / ".github" / "workflows"
    plan = [
        (workflow, target / f"{COMPONENT}-{workflow.name}")
        for workflow in sorted(source.iterdir())
        if workflow.is_file()
    ]
    taken = [destination for _, destination in plan if destination.exists()]
    if taken:
        listing = "\n".join(f"    {path.relative_to(ROOT)}" for path in taken)
        sys.stderr.write(
            f"\n  ERROR: component {COMPONENT!r} already has workflows in this repository:\n\n"
            f"{listing}\n\n"
            "  Nothing was overwritten. Generate it under another name, e.g. component=api2, or\n"
            "  remove the existing component first.\n\n"
        )
        sys.exit(1)
    target.mkdir(parents=True, exist_ok=True)
    for workflow, destination in plan:
        text = workflow.read_text()
        for token, value in TOKENS:
            text = text.replace(token, value)
        destination.write_text(text)
    shutil.rmtree(PROJECT / ".github")


def _remove(*relative_paths: str) -> None:
    for relative in relative_paths:
        target = PROJECT / relative
        if target.is_dir():
            shutil.rmtree(target)
        elif target.exists():
            target.unlink()


def main() -> None:
    # First, so every path below is the one the component has.
    _select_deploy_target()

    # Before anything else lands outside the folder: this is the step that refuses, and a refusal
    # must leave the repository as it found it. Cookiecutter deletes the component folder itself
    # when this hook exits non-zero.
    _install_workflows()
    _root_justfile()
    _root_agents(f"{PROJECT_NAME}: a FastAPI service in Python, deployed to {TARGET_NAMES[DEPLOY_TARGET]}")

    if not USE_SENTRY:
        _remove("backend/app/core/observability.py")

    if not USE_POSTGRES:
        # No database means no migrations, no ORM and no session to hand a repository. The layer
        # packages stay — an empty repositories/ is the signal of where data access goes when it
        # arrives — and readiness drops to the version check it already falls back to.
        _remove(
            "backend/alembic",
            "backend/alembic.ini",
            "backend/app/core/database.py",
            "backend/app/models/base.py",
            "backend/app/repositories/base.py",
            "backend/app/repositories/health_repository.py",
            "backend/tests/test_config_database.py",
            "backend/tests/test_database_auth.py",
        )
        # And no database means no users, which means no authentication. There is nowhere to put
        # an account and nothing to verify a password against, so the whole auth example goes
        # rather than shipping half of it: a service with no user table cannot be closed by
        # adding a dependency, and pretending otherwise is worse than leaving it open. Turning
        # `use_postgres` back on is a regeneration, and it brings all of this with it.
        _remove(
            "backend/app/api/refresh_cookie.py",
            "backend/app/api/v1/auth.py",
            "backend/app/api/v1/auth_body.py",
            "backend/app/api/v1/auth_cookie.py",
            "backend/app/core/security",
            "backend/app/models/refresh_session.py",
            "backend/app/models/user.py",
            "backend/app/repositories/refresh_session_repository.py",
            "backend/app/repositories/user_repository.py",
            "backend/app/schemas/auth.py",
            "backend/app/services/auth_service.py",
            "backend/tests/helpers.py",
            "backend/tests/test_auth_api.py",
            "backend/tests/test_auth_service.py",
            "backend/tests/test_migrations.py",
            "backend/tests/test_password_hashing.py",
            "backend/tests/test_refresh_cookie.py",
            "backend/tests/test_refresh_session_repository.py",
            "backend/tests/test_sessions_api.py",
            "backend/tests/test_signing_key.py",
            "backend/tests/test_tokens.py",
            "backend/tests/test_unit_of_work.py",
        )
        # No database means nothing to provision one for. The rest of the stack is unchanged:
        # a service, a registry, its identities and the secrets it reads. The AWS target also has
        # a migration task of its own, in a component the GCP target does not have; `_remove`
        # skips whichever of the two paths the chosen target never had.
        #
        # And no secret the stack needs a value for: the signing key and the database's admin
        # password are the two `infra/scripts/secrets.sh` fills, and both exist only with a
        # database. A Sentry DSN, if there is one, is a human's to add.
        _remove(
            "infra/pulumi/components/database.py",
            "infra/pulumi/components/migrations.py",
            "infra/scripts/secrets.sh",
        )

    for relative in EXECUTABLE:
        target = PROJECT / relative
        if target.exists():
            target.chmod(target.stat().st_mode | stat.S_IXUSR | stat.S_IXGRP | stat.S_IXOTH)

    secrets = (
        "just infra-secrets" + " " * 18 + "generates the signing key{% if cookiecutter.deploy_target == 'gcp-cloud-run' %} and the database password{% endif %}, pins the version"
        if USE_POSTGRES
        else "(nothing)" + " " * 27 + "no database, so no secret the stack generates"
    )
    #: The lockfile is not templated — a committed one would pin whatever existed when the
    #: template was last touched — and every image build installs with --frozen, so the first
    #: command in a new project is always resolving it.
    sys.stdout.write(
        "\n"
        f"  Created {COMPONENT}/, with .github/workflows/{COMPONENT}-*.yml and `mod {COMPONENT}` in\n"
        "  the root justfile. Run its recipes as `just <recipe>` inside the folder, or as\n"
        f"  `just {COMPONENT} <recipe>` from the repository root.\n"
        "\n"
        "  First commands:\n"
        "\n"
        f"    cd {COMPONENT} && cp .env.example .env\n"
        "    (cd backend && uv lock)        # the lockfile is not templated; resolve it once\n"
        "    just dev\n"
        "\n"
        "  Then: just check\n"
        "\n"
        "  Deploying is a separate sequence and it starts on your machine, not in CI. Read\n"
        f"  {COMPONENT}/infra/README.md before running anything under infra/: the first command\n"
        "  there creates cloud resources and costs money. The order, from inside the folder:\n"
        "\n"
        "    1. ./infra/bootstrap/state-bucket.sh   the state bucket and its key, once per account\n"
        "    2. pulumi -C infra/pulumi stack init   the bootstrap prints the exact command\n"
        "    3. {% if cookiecutter.deploy_target == 'aws-ecs-express' %}githubSubjectPrefix                 pulumi config set --plaintext; infra/README.md{% else %}(nothing on GCP)                    Workload Identity trusts the repository by name{% endif %}\n"
        "    4. just infra-up                       everything but the service\n"
        f"    5. {secrets}\n"
        "    6. just infra-first-deploy             the real image, the migrations, the service live\n"
        "    7. just infra-sync-github              copies the outputs into repository variables\n"
        "       protect `infrastructure`            required reviewers, main only; GitHub settings\n"
        f"    8. push                                CD owns every image, {COMPONENT}-infra.yml every\n"
        "                                           infrastructure change\n"
        "\n"
    )


main()
