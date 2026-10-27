"""Pre-generation checks. Cookiecutter runs this before it writes anything.

A value that is wrong here is cheap to refuse now and expensive to discover later: it is rendered
into the Pulumi backend URL, the provider's allowed-account guard and every resource name, and the
first place it would fail is a `pulumi up` against the wrong account.
"""

from __future__ import annotations

import re
import sys
from pathlib import Path

COMPONENT = "{{ cookiecutter.component }}"
DEPLOY_TARGET = "{{ cookiecutter.deploy_target }}"
AWS_ACCOUNT_ID = "{{ cookiecutter.aws_account_id }}"
AWS_REGION = "{{ cookiecutter.aws_region }}"

#: The component is the folder name, the workflow-file prefix, the repository-variable prefix and
#: the suffix of every cloud resource name. Lowercase and short, because it is spliced into names
#: with hard length limits (an IAM role, a GCP service account), and starting with a letter,
#: because an RDS identifier and a GitHub variable must.
COMPONENT_PATTERN = r"[a-z][a-z0-9-]{0,19}"

#: The workflows this component installs at the repository root. Checked here as well as in the
#: post-generation hook, because `cookiecutter -f` overwrites an existing component folder before
#: post-generation runs: the refusal has to come first. Cookiecutter runs this hook inside the
#: component's folder, so the repository root is its parent.
WORKFLOWS = ("ci.yml", "cd.yml", "infra.yml")
ROOT = Path.cwd().parent


def main() -> None:
    problems = []
    if not re.fullmatch(COMPONENT_PATTERN, COMPONENT):
        problems.append(
            f"component {COMPONENT!r} must match ^{COMPONENT_PATTERN}$: a lowercase letter, then up to "
            "19 lowercase letters, digits or hyphens"
        )
    taken = [ROOT / ".github" / "workflows" / f"{COMPONENT}-{name}" for name in WORKFLOWS]
    taken = [str(path.relative_to(ROOT)) for path in taken if path.exists()]
    if taken:
        problems.append(
            f"component {COMPONENT!r} is already in this repository ({', '.join(taken)}). Nothing was "
            f"written: generate it with a different name, e.g. component={COMPONENT}2, or remove "
            "those files first if they are left over"
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
