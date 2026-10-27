"""Pre-generation checks. Cookiecutter runs this before it writes anything.

A value that is wrong here is cheap to refuse now and expensive to discover later. The AWS account
and region are rendered into the Pulumi backend URL, the provider's allowed-account guard and the
site bucket's name, and the first place they would fail is a `pulumi up` against the wrong account.
The API URL is rendered into `.env.example` and the client's fallback, and a value that is not a URL
fails in a browser console rather than here.

The component is the folder this app is generated into, the prefix of its workflows and its
repository variables, and the second half of every cloud resource's name. It has to survive all
four: a directory, a workflow file name, an upper-cased GitHub variable name and an S3 bucket name.
"""

from __future__ import annotations

import re
import sys
from pathlib import Path

COMPONENT = "{{ cookiecutter.component }}"
DEPLOY_TARGET = "{{ cookiecutter.deploy_target }}"
AWS_ACCOUNT_ID = "{{ cookiecutter.aws_account_id }}"
AWS_REGION = "{{ cookiecutter.aws_region }}"
API_URL = "{{ cookiecutter.api_url }}"

#: The workflows every target installs, as `<repo-root>/.github/workflows/<component>-<name>`. The
#: post-generation hook makes the authoritative check against the files themselves; this one runs
#: before cookiecutter writes a single file, so `-f` over an existing component cannot half-overwrite
#: it first. Cookiecutter runs this hook from the component's folder, so the root is its parent.
WORKFLOWS = ("ci.yml", "cd.yml", "infra.yml")


def main() -> None:
    problems = []
    if not re.fullmatch(r"[a-z][a-z0-9-]{0,19}", COMPONENT):
        problems.append(
            f"component {COMPONENT!r} must be a lowercase letter followed by up to 19 lowercase"
            " letters, digits or hyphens, such as web"
        )
    if not re.fullmatch(r"https?://[^\s/]+(/[^\s]*)?", API_URL) or API_URL.endswith("/"):
        problems.append(
            f"api_url {API_URL!r} is not an http(s) origin such as http://localhost:8000"
            " (no trailing slash)"
        )
    if DEPLOY_TARGET == "aws-s3-cloudfront":
        if not re.fullmatch(r"[0-9]{12}", AWS_ACCOUNT_ID):
            problems.append(f"aws_account_id {AWS_ACCOUNT_ID!r} is not 12 digits")
        if not re.fullmatch(r"[a-z]{2}(-gov)?-[a-z]+-[0-9]", AWS_REGION):
            problems.append(f"aws_region {AWS_REGION!r} is not a region code such as eu-west-1")
    taken = [
        f".github/workflows/{COMPONENT}-{name}"
        for name in WORKFLOWS
        if (Path.cwd().parent / ".github" / "workflows" / f"{COMPONENT}-{name}").exists()
    ]
    if taken:
        problems.append(
            f"this repository already has a component named {COMPONENT!r} ({', '.join(taken)}):"
            f" generate with a different component, for example component={COMPONENT}2"
        )
    if problems:
        sys.stderr.write("\n".join(f"  ERROR: {p}" for p in problems) + "\n")
        sys.exit(1)


main()
