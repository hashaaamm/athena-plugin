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
)

INCLUDE_FRONTEND = "{{ cookiecutter.include_frontend }}" == "yes"
USE_POSTGRES = "{{ cookiecutter.use_postgres }}" == "yes"
USE_SENTRY = "{{ cookiecutter.use_sentry }}" == "yes"
SLUG = "{{ cookiecutter.project_slug }}"


def _remove(*relative_paths: str) -> None:
    for relative in relative_paths:
        target = PROJECT / relative
        if target.is_dir():
            shutil.rmtree(target)
        elif target.exists():
            target.unlink()


def main() -> None:
    if not INCLUDE_FRONTEND:
        # The backend does not reference frontend/ anywhere, so removing it is a clean cut.
        # Adding it back later means creating the directory, not restructuring the repo.
        # Both its workflows and its CORS test go with it: a pipeline filtered on a path that does
        # not exist never runs and never says so, and CORS is only a question once a browser asks.
        _remove(
            "frontend",
            ".github/workflows/frontend-ci.yml",
            ".github/workflows/frontend-cd.yml",
            "backend/tests/test_cors.py",
        )

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
        )
        # And no database means no users, which means no authentication. There is nowhere to put
        # an account and nothing to verify a password against, so the whole auth example goes
        # rather than shipping half of it: a service with no user table cannot be closed by
        # adding a dependency, and pretending otherwise is worse than leaving it open. Turning
        # `use_postgres` back on is a regeneration, and it brings all of this with it.
        _remove(
            "backend/app/api/v1/auth.py",
            "backend/app/core/security",
            "backend/app/models/user.py",
            "backend/app/repositories/user_repository.py",
            "backend/app/schemas/auth.py",
            "backend/app/services/auth_service.py",
            "backend/tests/helpers.py",
            "backend/tests/test_auth_api.py",
            "backend/tests/test_auth_service.py",
            "backend/tests/test_password_hashing.py",
            "backend/tests/test_signing_key.py",
            "backend/tests/test_tokens.py",
        )
        # The web client's half of the same cut. A sign-in form posting to an endpoint that was
        # deleted three paragraphs ago is worse than no sign-in form: it compiles, it renders,
        # and it fails in a browser. The shared files — router, shell, API client, README and
        # AGENTS.md — branch in Jinja instead, because they exist either way. Harmless when
        # `include_frontend` is "no": frontend/ has already gone and `_remove` skips what is
        # not there. The dashboard's route test goes with them: it asserts the guard and mounts
        # the real tree through `test-router.tsx`, and neither exists here. What that page still
        # owns without a database — the shortened build identifier — is covered by the unit test
        # in `lib/format.test.ts`, which every variant keeps.
        _remove(
            "frontend/src/components/auth-card.tsx",
            "frontend/src/lib/api/auth.ts",
            "frontend/src/lib/api/auth.test.ts",
            "frontend/src/lib/auth.ts",
            "frontend/src/lib/auth.test.ts",
            "frontend/src/lib/use-session.ts",
            "frontend/src/routes/account.tsx",
            "frontend/src/routes/account.test.tsx",
            "frontend/src/routes/dashboard.test.tsx",
            "frontend/src/routes/login.tsx",
            "frontend/src/routes/login.test.tsx",
            "frontend/src/routes/register.tsx",
            "frontend/src/routes/register.test.tsx",
            "frontend/src/test-router.tsx",
        )
        # No database means nothing to provision one for. The rest of the stack is unchanged:
        # a service, a registry, two identities and the secrets it reads.
        _remove("infra/pulumi/components/database.py")

    for relative in EXECUTABLE:
        target = PROJECT / relative
        if target.exists():
            target.chmod(target.stat().st_mode | stat.S_IXUSR | stat.S_IXGRP | stat.S_IXOTH)

    #: Neither lockfile is templated — a committed one would pin whatever existed when the
    #: template was last touched — and every image build installs with a --frozen flag, so the
    #: first command in a new project is always resolving them.
    frontend_lock = (
        "    (cd frontend && just lock)     # same, for pnpm-lock.yaml\n" if INCLUDE_FRONTEND else ""
    )

    sys.stdout.write(
        "\n"
        f"  Created {SLUG}/\n"
        "\n"
        "  First commands:\n"
        "\n"
        f"    cd {SLUG} && cp .env.example .env\n"
        "    (cd backend && uv lock)        # the lockfile is not templated; resolve it once\n"
        f"{frontend_lock}"
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
