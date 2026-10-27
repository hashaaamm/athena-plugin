"""Post-generation clean-up, and the component's place in the repository around it.

Cookiecutter renders the whole tree into `<repo-root>/<component>/` and then runs this, from that
directory. Deleting here rather than branching in Jinja keeps the templates readable: a file either
exists for every project or it is removed once, in one place, by a rule you can read.

A repository holds one folder per component (ADR-0024), and this app is one of them. Three things
cannot live inside the folder, because GitHub and `just` look for them at the repository root: the
workflows, the root justfile and the root AGENTS.md. This hook puts the component's share of each
there, and refuses rather than overwrite another component's.
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
PROJECT_NAME = "{{ cookiecutter.project_name }}"

#: Scripts a human runs directly. Cookiecutter writes rendered files with the default mode, so the
#: executable bit has to be set here or `./infra/bootstrap/state-bucket.sh` fails with "permission
#: denied" on the first command of the whole deployment sequence.
EXECUTABLE = (
    "infra/bootstrap/state-bucket.sh",
    "infra/bootstrap/teardown.sh",
    "infra/scripts/sync-github.sh",
    "infra/scripts/first-deploy.sh",
    "infra/scripts/publish.sh",
)

INCLUDE_AUTH = "{{ cookiecutter.include_auth }}" == "yes"

DEPLOY_TARGET = "{{ cookiecutter.deploy_target }}"
#: How the root AGENTS.md names each deploy target.
TARGET_NAMES = {"aws-ecs-express": "ECS Express Mode", "gcp-cloud-run": "Cloud Run", "aws-s3-cloudfront": "S3 and CloudFront"}

#: The only templating in a workflow. Workflows are `_copy_without_render`, because an Actions
#: expression and a Jinja one are written with the same two braces, so the component's name reaches
#: them as these plain tokens instead. The longer token first: it is not a substring of the shorter
#: one today, and replacing it first keeps that from ever mattering.
TOKENS = (
    ("__COMPONENT_VAR__", COMPONENT_VAR),
    ("__COMPONENT__", COMPONENT),
)


def _select_deploy_target() -> None:
    """Move the chosen target's stack and pipelines into place, and drop every other target.

    Each target under deploy/ is a complete implementation of the same interface: an infra/
    directory whose justfile has the same recipes (lock, test, lint, preview, up, first-deploy,
    sync-github, destroy, teardown), and the deploy workflows those recipes un-gate. The component
    has exactly one of them, at the paths every other file already refers to.
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


def _workflow_sources() -> list[Path]:
    """The workflows this component installs: the shared ones and the chosen target's."""
    shared = PROJECT / ".github" / "workflows"
    chosen = PROJECT / "deploy" / DEPLOY_TARGET / "workflows"
    found = [*shared.iterdir()] + ([*chosen.iterdir()] if chosen.is_dir() else [])
    return sorted(found, key=lambda path: path.name)


def _destination(workflow: Path) -> Path:
    return ROOT / ".github" / "workflows" / f"{COMPONENT}-{workflow.name}"


def _refuse_taken_workflows() -> None:
    """Stop before anything moves when another component already owns these workflow names.

    Run first, so a refusal leaves the repository exactly as it was — and cookiecutter then removes
    the component folder it created. (Generating into a folder that already exists is refused by
    cookiecutter itself, unless it is told to overwrite; this is what stops that, too.)
    """
    taken = [_destination(path) for path in _workflow_sources() if _destination(path).exists()]
    if not taken:
        return
    names = "\n".join(f"    {d.relative_to(ROOT)}" for d in taken)
    sys.stderr.write(
        "\n"
        f"  ERROR: this repository already has workflows for a component named {COMPONENT!r}:\n"
        "\n"
        f"{names}\n"
        "\n"
        "  Nothing was installed. Generate again with a different component, for example\n"
        f"  component={COMPONENT}2, or remove the other component first.\n"
        "\n"
    )
    sys.exit(1)


def _install_workflows() -> None:
    """Copy the component's workflows to `<repo-root>/.github/workflows/<component>-<file>`."""
    source = PROJECT / ".github" / "workflows"
    (ROOT / ".github" / "workflows").mkdir(parents=True, exist_ok=True)
    for path in sorted(source.iterdir()):
        text = path.read_text(encoding="utf-8")
        for token, value in TOKENS:
            text = text.replace(token, value)
        _destination(path).write_text(text, encoding="utf-8")
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
#: file would describe a deployment this component does not have. The SPA fallback, the API proxy
#: and the cache headers they carry on GCP are the distribution's routing function, its second
#: origin and the publish script's metadata there — `infra/README.md` says where each one went.
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
    # First, because it is the step that can refuse, and a refusal must come before anything moves.
    _refuse_taken_workflows()
    # Then the target, so every path below is the one the component has.
    _select_deploy_target()
    _install_workflows()
    _root_justfile()
    _root_agents(f"{PROJECT_NAME}: a React single-page app, deployed to {TARGET_NAMES[DEPLOY_TARGET]}")

    if not INCLUDE_AUTH:
        _remove(*WITHOUT_AUTH)
    _remove(*WITHOUT_TARGET[DEPLOY_TARGET])

    for relative in EXECUTABLE:
        target = PROJECT / relative
        if target.exists():
            target.chmod(target.stat().st_mode | stat.S_IXUSR | stat.S_IXGRP | stat.S_IXOTH)

    aws = DEPLOY_TARGET == "aws-s3-cloudfront"
    subject = (
        "    pulumi -C infra/pulumi config set --plaintext githubSubjectPrefix '<gh api …/oidc/customization/sub>'\n"
        if aws
        else ""
    )
    #: Neither lockfile is templated — a committed one would pin whatever existed when the template
    #: was last touched — and every install runs with --frozen-lockfile, so the first commands in a
    #: new component resolve them.
    sys.stdout.write(
        "\n"
        f"  Created {COMPONENT}/ in {ROOT}\n"
        f"  Workflows: .github/workflows/{COMPONENT}-*.yml   Recipes: just {COMPONENT} <recipe>\n"
        "\n"
        "  First commands:\n"
        "\n"
        f"    cd {COMPONENT}\n"
        "    just lock                 # resolves pnpm-lock.yaml in a container; commit it\n"
        "    (cd infra && just lock)   # the stack's own lockfile, on the host; commit it too\n"
        "    just dev                  # http://localhost:3000, calling {{ cookiecutter.api_url }}\n"
        "\n"
        f"  Then: just check   (from the repository root: just {COMPONENT} check)\n"
        "\n"
        "  The backend this app calls has to allow it: its CORS_ORIGINS must include\n"
        "  http://localhost:3000"
        + (", and REFRESH_TRANSPORT=cookie for the sign-in screens" if INCLUDE_AUTH else "")
        + ".\n"
        "\n"
        "  Deploying is a separate sequence and it starts on your machine, not in CI. Read\n"
        f"  {COMPONENT}/infra/README.md before running any of it: the first command creates cloud\n"
        f"  resources and costs money. From {COMPONENT}/, in this order:\n"
        "\n"
        "    ./infra/bootstrap/state-bucket.sh\n"
        "    pulumi -C infra/pulumi stack init dev --secrets-provider=…   # the bootstrap prints it\n"
        + subject
        + "    pulumi -C infra/pulumi config set --plaintext apiUrl https://<the backend's URL>\n"
        "    just infra-up                 # "
        + ("the bucket, the distribution and the roles" if aws else "everything but the Cloud Run service")
        + "\n"
        "    just infra-first-deploy       # "
        + ("builds the bundle, uploads it and smoke-tests it" if aws else "builds and pushes the image, then creates the service on it")
        + "\n"
        "    just infra-sync-github        # copies the stack's outputs into "
        + f"{COMPONENT_VAR}_* repository variables\n"
        "    (protect `infrastructure`)    # required reviewers, main only; GitHub settings\n"
        "    git push                      # CD owns every deploy, "
        + f"{COMPONENT}-infra.yml every infra change\n"
        "\n"
        "  Deployed, the site serves the API on its own origin — /api/ and /health/ are forwarded to\n"
        "  apiUrl by "
        + ("the distribution" if aws else "the image's nginx")
        + " (apiSameOrigin, on by default) — so the app and the API are one site.\n"
        + (
            "  That is what lets the SameSite=Strict refresh cookie survive a reload. On the backend's\n"
            "  stack, set refreshTransport=cookie, and corsOrigins to this stack's site_url: the cookie\n"
            "  routes' CSRF check accepts only the Origins listed there, same-origin ones included.\n"
            if INCLUDE_AUTH
            else "  The backend needs no corsOrigins for it: same-origin requests need no CORS.\n"
        )
        + "  A domain of your own for both is still the better production setup; infra/README.md.\n"
        "\n"
    )


main()
