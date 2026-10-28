"""Post-generation: the component's place in the repository around it.

Cookiecutter renders the whole tree into `<repo-root>/<component>/` and then runs this, with that
folder as the working directory.

Three things land outside the folder, and they are the only three: the component's workflows, under
the repository's one `.github/workflows/`, prefixed with the component's name; a `mod` line in the
root `justfile`; and a line in the root `AGENTS.md`. Each is written so a second component generated
into the same repository adds to it rather than colliding with it, and so the same component
generated twice is refused before anything is overwritten.

`_root_justfile`, `_root_agents` and `_install_workflows` are the service templates' functions,
copied unchanged from `cookiecutter-fastapi-service/hooks/post_gen_project.py`: whichever component
is generated first writes the root files, so every template has to write them the same way.
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
CLUSTER_MODE = "{{ cookiecutter.cluster_mode }}"

#: The only templating in a workflow file. Workflows are copied without Jinja rendering, because a
#: GitHub Actions expression and a Jinja one are written with the same two braces; these two tokens
#: are substituted as plain text instead. `__COMPONENT_VAR__` first: it is the longer token, and a
#: repository variable is written `vars.__COMPONENT_VAR___WIF_PROVIDER`.
TOKENS = (
    ("__COMPONENT_VAR__", COMPONENT_VAR),
    ("__COMPONENT__", COMPONENT),
)

#: Scripts a human runs directly. Cookiecutter writes rendered files with the default mode, so the
#: executable bit has to be set here or the first command of the deployment sequence fails with
#: "permission denied".
EXECUTABLE = (
    "infra/bootstrap/state-bucket.sh",
    "infra/scripts/tf.sh",
    "infra/scripts/up.sh",
    "infra/scripts/sync-github.sh",
    "infra/scripts/teardown.sh",
)

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


def _install_workflows() -> None:
    """Copy the component's workflows to the repository's `.github/workflows/`, prefixed.

    GitHub reads workflows from the repository root and nowhere else, so this is the one part of a
    component that cannot live in its folder. Each file becomes `<component>-<name>`, with the two
    tokens substituted. Nothing is overwritten: a destination that exists means this component
    name is taken, and the whole generation is refused before a single file is written, because a
    half-replaced pipeline applies one component with another's configuration.
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
            "  Nothing was overwritten. Generate it under another name, e.g. component=langfuse2,\n"
            "  or remove the existing component first.\n\n"
        )
        sys.exit(1)
    target.mkdir(parents=True, exist_ok=True)
    for workflow, destination in plan:
        text = workflow.read_text()
        for token, value in TOKENS:
            text = text.replace(token, value)
        destination.write_text(text)
    shutil.rmtree(PROJECT / ".github")


def main() -> None:
    # First: this is the step that refuses, and a refusal must leave the repository as it found
    # it. Cookiecutter deletes the component folder itself when this hook exits non-zero.
    _install_workflows()
    _root_justfile()
    where = "a new GKE Autopilot cluster" if CLUSTER_MODE == "new" else "an existing GKE cluster"
    _root_agents(
        f"{PROJECT_NAME}: self-hosted Langfuse on GCP, Terraform with the official module, on {where}"
    )

    for relative in EXECUTABLE:
        target = PROJECT / relative
        if target.exists():
            target.chmod(target.stat().st_mode | stat.S_IXUSR | stat.S_IXGRP | stat.S_IXOTH)

    sys.stdout.write(
        "\n"
        f"  Created {COMPONENT}/, with .github/workflows/{COMPONENT}-*.yml and `mod {COMPONENT}` in\n"
        "  the root justfile. Run its recipes as `just <recipe>` inside the folder, or as\n"
        f"  `just {COMPONENT} <recipe>` from the repository root.\n"
        "\n"
        "  Nothing has been created in the cloud. Check what was generated, without credentials:\n"
        "\n"
        f"    cd {COMPONENT} && just lint\n"
        "\n"
        f"  Then read {COMPONENT}/README.md before running anything else: the next command creates\n"
        "  cloud resources, and the whole stack costs money every hour it exists. The order:\n"
        "\n"
        "    1. just bootstrap      APIs, the KMS key and the state bucket (or checks an existing one)\n"
        "    2. just init           points Terraform at that bucket\n"
        "    3. just plan           read it\n"
        "    4. just up             the first apply, from your machine, once\n"
        "    5. DNS                 delegate the hostname to the name servers `just up` prints\n"
        "    6. just sync-github    repository variables; protect the `infrastructure` environment\n"
        f"    7. push                {COMPONENT}-infra.yml plans every pull request, applies every merge\n"
        "\n"
    )


main()
