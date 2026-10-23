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

#: Scripts a human or a pipeline runs directly. Cookiecutter writes rendered files with the default
#: mode, so the executable bit has to be set here or the first command of the deployment sequence
#: fails with "permission denied".
EXECUTABLE = (
    "infra/bootstrap/state-bucket.sh",
    "infra/bootstrap/teardown.sh",
    "infra/scripts/sync-github.sh",
)

USE_POSTGRES = "{{ cookiecutter.use_postgres }}" == "yes"
USE_SENTRY = "{{ cookiecutter.use_sentry }}" == "yes"
SLUG = "{{ cookiecutter.project_slug }}"

# --- backend -------------------------------------------------------------------------------------

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

# --- infra ---------------------------------------------------------------------------------------

#: Nothing to provision a database for. The rest of the stack is unchanged: a service, a registry,
#: two identities and the secret containers it reads. The shared files — main.go, the config, the
#: API list, the runtime roles — branch in Jinja instead, because they exist either way.
INFRA_WITHOUT_POSTGRES: tuple[str, ...] = (
    "infra/pulumi/internal/components/database.go",
    "infra/pulumi/internal/components/database_test.go",
    "infra/pulumi/main_postgres_test.go",
)

#: The stack keeps no Sentry resource of its own beyond one empty secret container, which the
#: Jinja in secrets.go already leaves out; only the tests about it go.
INFRA_WITHOUT_SENTRY: tuple[str, ...] = (
    "infra/pulumi/internal/components/sentry_test.go",
    "infra/pulumi/main_sentry_test.go",
)


def _remove(*relative_paths: str) -> None:
    for relative in relative_paths:
        target = PROJECT / relative
        if target.is_dir():
            shutil.rmtree(target)
        elif target.exists():
            target.unlink()


def main() -> None:
    if not USE_POSTGRES:
        _remove(*BACKEND_WITHOUT_POSTGRES, *INFRA_WITHOUT_POSTGRES)
    if not USE_SENTRY:
        _remove(*BACKEND_WITHOUT_SENTRY, *INFRA_WITHOUT_SENTRY)

    for relative in EXECUTABLE:
        target = PROJECT / relative
        if target.exists():
            target.chmod(target.stat().st_mode | stat.S_IXUSR | stat.S_IXGRP | stat.S_IXOTH)

    sys.stdout.write(
        "\n"
        f"  Created {SLUG}/\n"
        "\n"
        "  First commands:\n"
        "\n"
        f"    cd {SLUG} && cp .env.example .env\n"
        "    (cd backend && just lock)      # go.sum is not templated; resolve it once\n"
        "    (cd infra && just lock)        # the same, for the Pulumi program\n"
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
