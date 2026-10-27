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
    "infra/bootstrap/teardown.sh",
    "infra/scripts/sync-github.sh",
)

INCLUDE_AUTH = "{{ cookiecutter.include_auth }}" == "yes"
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


#: The signed-in half of the application. A sign-in form posting to a backend with no user table
#: is worse than no sign-in form: it compiles, it renders, and it fails in a browser. The shared
#: files — router, shell, API client, README and AGENTS.md — branch in Jinja instead, because they
#: exist either way. The dashboard's route test goes with them: it asserts the guard and mounts the
#: real tree through `test-router.tsx`, and neither exists here. What that page still owns without
#: auth — the shortened build identifier — is covered by the unit test in `lib/format.test.ts`,
#: which every variant keeps.
WITHOUT_AUTH: tuple[str, ...] = (
    "src/components/auth-card.tsx",
    "src/lib/api/auth.ts",
    "src/lib/api/auth.test.ts",
    "src/lib/auth.ts",
    "src/lib/auth.test.ts",
    "src/lib/use-session.ts",
    "src/routes/account.tsx",
    "src/routes/account.test.tsx",
    "src/routes/dashboard.test.tsx",
    "src/routes/login.tsx",
    "src/routes/login.test.tsx",
    "src/routes/register.tsx",
    "src/routes/register.test.tsx",
    "src/test-router.tsx",
)

#: Keyed by deploy target. On AWS the bundle is files in a bucket, served by CloudFront: there is
#: no container, so the production image, its nginx configuration and the build context's ignore
#: file would describe a deployment this repository does not have. The SPA fallback and the cache
#: headers they carry on GCP are the distribution's error responses and the CD sync's metadata
#: there — `infra/README.md` says where each one went.
WITHOUT_TARGET: dict[str, tuple[str, ...]] = {
    "gcp-cloud-run": (),
    "aws-s3-cloudfront": ("docker", ".dockerignore"),
}


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

    if not INCLUDE_AUTH:
        _remove(*WITHOUT_AUTH)
    _remove(*WITHOUT_TARGET[DEPLOY_TARGET])

    for relative in EXECUTABLE:
        target = PROJECT / relative
        if target.exists():
            target.chmod(target.stat().st_mode | stat.S_IXUSR | stat.S_IXGRP | stat.S_IXOTH)

    #: Neither lockfile is templated — a committed one would pin whatever existed when the template
    #: was last touched — and every install runs with --frozen-lockfile, so the first commands in a
    #: new project resolve them.
    sys.stdout.write(
        "\n"
        f"  Created {SLUG}/\n"
        "\n"
        "  First commands:\n"
        "\n"
        f"    cd {SLUG}\n"
        "    just lock                 # resolves pnpm-lock.yaml in a container; commit it\n"
        "    (cd infra && just lock)   # the stack's own lockfile, on the host; commit it too\n"
        "    just dev                  # http://localhost:3000, calling {{ cookiecutter.api_url }}\n"
        "\n"
        "  Then: just check\n"
        "\n"
        "  The backend this app calls has to allow it: its CORS_ORIGINS must include\n"
        "  http://localhost:3000"
        + (", and REFRESH_TRANSPORT=cookie for the sign-in screens" if INCLUDE_AUTH else "")
        + ".\n"
        "\n"
        "  Deploying is a separate sequence and it starts on your machine, not in CI.\n"
        f"  Read {SLUG}/infra/README.md before running anything under infra/: the first\n"
        "  command there creates cloud resources and costs money.\n"
        "\n"
    )


main()
