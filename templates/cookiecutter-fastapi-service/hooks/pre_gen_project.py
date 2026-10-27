"""Pre-generation checks. Cookiecutter runs this before it writes anything.

A value that is wrong here is cheap to refuse now and expensive to discover later: it is rendered
into the Pulumi backend URL, the provider's allowed-account guard and every resource name, and the
first place it would fail is a `pulumi up` against the wrong account.
"""

from __future__ import annotations

import re
import sys
from pathlib import Path

#: The component is a folder name, a workflow filename prefix, a `just` module name, part of every
#: cloud resource name and — upper-cased — the prefix of every repository variable it reads. This is
#: the shape all five accept: lowercase, a letter first, short enough to leave room in names that
#: are capped at 30 characters.
COMPONENT_PATTERN = r"[a-z][a-z0-9-]{0,19}"

#: The workflows the post-generation hook installs at the repository root, by template name.
WORKFLOWS = ("ci.yml", "cd.yml", "infra.yml")

COMPONENT = "{{ cookiecutter.component }}"
DEPLOY_TARGET = "{{ cookiecutter.deploy_target }}"
AWS_ACCOUNT_ID = "{{ cookiecutter.aws_account_id }}"
AWS_REGION = "{{ cookiecutter.aws_region }}"


def main() -> None:
    problems = []
    if not re.fullmatch(COMPONENT_PATTERN, COMPONENT):
        problems.append(
            f"component {COMPONENT!r} must match ^{COMPONENT_PATTERN}$: lowercase letters, digits "
            "and hyphens, starting with a letter, at most 20 characters"
        )
    else:
        # The same component twice. Cookiecutter refuses an existing folder on its own, but not
        # with `-f`, which overwrites the folder's files before the post-generation hook could
        # refuse — so the workflows, the one thing outside the folder, are checked here first.
        # The working directory is `<repo-root>/<component>/`.
        workflows = Path.cwd().parent / ".github" / "workflows"
        taken = [
            f"{COMPONENT}-{name}"
            for name in WORKFLOWS
            if (workflows / f"{COMPONENT}-{name}").exists()
        ]
        if taken:
            problems.append(
                f"component {COMPONENT!r} already exists in this repository "
                f"(.github/workflows/{', '.join(taken)}); nothing was written. Generate it under "
                "another name, e.g. component=api2"
            )
    if DEPLOY_TARGET == "aws-ecs-express":
        if not re.fullmatch(r"[0-9]{12}", AWS_ACCOUNT_ID):
            problems.append(f"aws_account_id {AWS_ACCOUNT_ID!r} is not 12 digits")
        if not re.fullmatch(r"[a-z]{2}(-gov)?-[a-z]+-[0-9]", AWS_REGION):
            problems.append(f"aws_region {AWS_REGION!r} is not a region code such as eu-west-1")
    if problems:
        sys.stderr.write("\n".join(f"  ERROR: {p}" for p in problems) + "\n")
        sys.exit(1)


main()
