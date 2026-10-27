"""Post-generation clean-up.

Cookiecutter renders the whole tree and then runs this. Deleting here rather than branching in
Jinja keeps the templates readable: a file either exists for every project or it is removed once,
in one place, by a rule you can read.
"""

from __future__ import annotations

import shutil
import stat
import sys
from pathlib import Path

PROJECT = Path.cwd()

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
    "infra/scripts/sync-github.sh",
    "infra/scripts/secrets.sh",
)

USE_POSTGRES = "{{ cookiecutter.use_postgres }}" == "yes"
USE_SENTRY = "{{ cookiecutter.use_sentry }}" == "yes"
SLUG = "{{ cookiecutter.project_slug }}"

DEPLOY_TARGET = "{{ cookiecutter.deploy_target }}"


def _select_deploy_target() -> None:
    """Move the chosen target's stack and pipelines into place, and drop every other target.

    Each target under deploy/ is a complete implementation of the same interface: an infra/
    directory whose justfile has the same recipes (lock, test, lint, preview, up, sync-github,
    destroy, teardown), and the deploy workflows those recipes un-gate. The generated repository
    has exactly one of them, at the paths every other file already refers to — infra/ and
    .github/workflows/ — so nothing outside deploy/ needs to know which target was chosen.
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


def _remove(*relative_paths: str) -> None:
    for relative in relative_paths:
        target = PROJECT / relative
        if target.is_dir():
            shutil.rmtree(target)
        elif target.exists():
            target.unlink()


def main() -> None:
    # First, so every path below is the one the generated repository has.
    _select_deploy_target()

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

    #: The lockfile is not templated — a committed one would pin whatever existed when the
    #: template was last touched — and every image build installs with --frozen, so the first
    #: command in a new project is always resolving it.
    sys.stdout.write(
        "\n"
        f"  Created {SLUG}/\n"
        "\n"
        "  First commands:\n"
        "\n"
        f"    cd {SLUG} && cp .env.example .env\n"
        "    (cd backend && uv lock)        # the lockfile is not templated; resolve it once\n"
        "    just dev\n"
        "\n"
        "  Then: just check\n"
        "\n"
        "  Deploying is a separate sequence and it starts on your machine, not in CI.\n"
        f"  Read {SLUG}/infra/README.md before running anything under infra/: the first\n"
        "  command there creates cloud resources and costs money.\n"
        "\n"
    )


main()
