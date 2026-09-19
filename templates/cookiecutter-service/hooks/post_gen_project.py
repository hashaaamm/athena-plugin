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

#: Scripts a human runs directly. Cookiecutter writes rendered files with the default mode, so the
#: executable bit has to be set here or `./infra/bootstrap/state-bucket.sh` fails with "permission
#: denied" on the first command of the whole deployment sequence.
EXECUTABLE = (
    "infra/bootstrap/state-bucket.sh",
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
        # Its workflow and its CORS test go with it: a pipeline filtered on a path that does not
        # exist never runs and never says so, and CORS is only a question once a browser asks it.
        _remove(
            "frontend",
            ".github/workflows/frontend-ci.yml",
            "backend/tests/test_cors.py",
        )
    elif not USE_POSTGRES:
        # The frontend's example resource is the backend's example resource. With no database
        # there is no /api/v1/items to call, so the pages that call it go too — the dashboard
        # and the generated API client are written to work either way.
        _remove(
            "frontend/src/lib/api/items.ts",
            "frontend/src/lib/api/items.test.ts",
            "frontend/src/routes/items.tsx",
            "frontend/src/routes/items.test.tsx",
        )

    if not USE_SENTRY:
        _remove("backend/app/core/observability.py")

    if not USE_POSTGRES:
        # No database means no migrations and no example resource to persist. The layer packages
        # stay — an empty repositories/ is the signal of where data access goes when it arrives.
        _remove(
            "backend/alembic",
            "backend/alembic.ini",
            "backend/app/core/database.py",
            "backend/app/models/base.py",
            "backend/app/repositories/health_repository.py",
            "backend/tests/helpers.py",
            "backend/app/models/item.py",
            "backend/app/repositories/base.py",
            "backend/app/repositories/item_repository.py",
            "backend/app/services/item_service.py",
            "backend/app/facades/item_facade.py",
            "backend/app/schemas/item.py",
            "backend/app/api/v1/item.py",
            "backend/app/seed.py",
            "backend/tests/test_config_database.py",
            "backend/tests/test_item_api.py",
            "backend/tests/test_item_service.py",
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
