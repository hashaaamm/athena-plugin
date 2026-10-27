"""Pre-generation checks. Cookiecutter runs this before it writes anything.

A value that is wrong here is cheap to refuse now and expensive to discover later: it is rendered
into the Pulumi backend URL, the provider's allowed-account guard and every resource name, and the
first place it would fail is a `pulumi up` against the wrong account.
"""

from __future__ import annotations

import re
import sys

DEPLOY_TARGET = "{{ cookiecutter.deploy_target }}"
AWS_ACCOUNT_ID = "{{ cookiecutter.aws_account_id }}"
AWS_REGION = "{{ cookiecutter.aws_region }}"


def main() -> None:
    if DEPLOY_TARGET != "aws-ecs-express":
        return
    problems = []
    if not re.fullmatch(r"[0-9]{12}", AWS_ACCOUNT_ID):
        problems.append(f"aws_account_id {AWS_ACCOUNT_ID!r} is not 12 digits")
    if not re.fullmatch(r"[a-z]{2}(-gov)?-[a-z]+-[0-9]", AWS_REGION):
        problems.append(f"aws_region {AWS_REGION!r} is not a region code such as eu-west-1")
    if problems:
        sys.stderr.write("\n".join(f"  ERROR: {p}" for p in problems) + "\n")
        sys.exit(1)


main()
