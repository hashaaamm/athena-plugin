"""Post-generation clean-up.

Cookiecutter renders the whole tree and then runs this. Deleting here rather than branching in
Jinja keeps the templates readable: a file either exists for every project or it is removed once,
in one place, by a rule you can read.
"""

from __future__ import annotations

import shutil
import sys
from pathlib import Path

PROJECT = Path.cwd()

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
        _remove("frontend")

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

    sys.stdout.write(
        "\n"
        f"  Created {SLUG}/\n"
        "\n"
        "  Next three commands:\n"
        "\n"
        f"    cd {SLUG}/backend && uv lock   # the lockfile is not templated; resolve it once\n"
        f"    cd {SLUG} && cp .env.example .env\n"
        "    just dev\n"
        "\n"
        "  Then: just check\n"
        "\n"
    )


main()
