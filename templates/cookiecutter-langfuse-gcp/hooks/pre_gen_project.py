"""Pre-generation checks. Cookiecutter runs this before it writes anything.

A value that is wrong here is cheap to refuse now and expensive to discover later: it is rendered
into the Terraform backend configuration, the variable file every plan reads and the name of every
cloud resource, and the first place it would fail is an apply against the wrong project.
"""

from __future__ import annotations

import re
import sys
from pathlib import Path

#: The component is a folder name, a workflow filename prefix, a `just` module name, the prefix of
#: every cloud resource name and — upper-cased — the prefix of every repository variable it reads.
#: Between 6 and 16 characters, not the service templates' 1 to 20: the official module names a
#: service account after it, and GCP wants a service-account id of 6 to 30 characters; this stack
#: adds `-apply-<env>` to two more ids under the same cap.
COMPONENT_PATTERN = r"[a-z][a-z0-9-]{5,15}"

#: The workflows the post-generation hook installs at the repository root, by template name.
WORKFLOWS = ("ci.yml", "infra.yml")

COMPONENT = "{{ cookiecutter.component }}"
GCP_PROJECT_ID = "{{ cookiecutter.gcp_project_id }}"
GCP_REGION = "{{ cookiecutter.gcp_region }}"
GITHUB_REPOSITORY = "{{ cookiecutter.github_repository }}"
HOSTNAME = "{{ cookiecutter.hostname }}"
CLUSTER_MODE = "{{ cookiecutter.cluster_mode }}"
EXISTING_CLUSTER_NAME = "{{ cookiecutter.existing_cluster_name }}"
EXISTING_CLUSTER_LOCATION = "{{ cookiecutter.existing_cluster_location }}"
STATE_BUCKET = "{{ cookiecutter.state_bucket }}"


def main() -> None:
    problems = []
    if not re.fullmatch(COMPONENT_PATTERN, COMPONENT) or COMPONENT.endswith("-"):
        problems.append(
            f"component {COMPONENT!r} must match ^{COMPONENT_PATTERN}$: lowercase letters, digits "
            "and hyphens, starting with a letter, not ending in a hyphen, 6 to 16 characters"
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
                "another name, e.g. component=langfuse2"
            )
    if not re.fullmatch(r"[a-z][a-z0-9-]{4,28}[a-z0-9]", GCP_PROJECT_ID):
        problems.append(f"gcp_project_id {GCP_PROJECT_ID!r} is not a GCP project id")
    if not re.fullmatch(r"[a-z]+-[a-z]+[0-9]", GCP_REGION):
        problems.append(f"gcp_region {GCP_REGION!r} is not a region such as europe-west1")
    if not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", GITHUB_REPOSITORY):
        problems.append(f"github_repository {GITHUB_REPOSITORY!r} is not owner/name")
    if not re.fullmatch(r"([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z]{2,}", HOSTNAME):
        problems.append(f"hostname {HOSTNAME!r} is not a lowercase DNS name such as langfuse.example.com")
    if not re.fullmatch(r"[a-z0-9][a-z0-9._-]{1,61}[a-z0-9]", STATE_BUCKET):
        problems.append(f"state_bucket {STATE_BUCKET!r} is not a GCS bucket name")
    if CLUSTER_MODE == "existing":
        if not EXISTING_CLUSTER_NAME:
            problems.append("cluster_mode=existing needs existing_cluster_name")
        if not re.fullmatch(r"[a-z]+-[a-z]+[0-9](-[a-z])?", EXISTING_CLUSTER_LOCATION):
            problems.append(
                f"existing_cluster_location {EXISTING_CLUSTER_LOCATION!r} is not a region or zone "
                "such as europe-west1 or europe-west1-b"
            )
    elif EXISTING_CLUSTER_NAME or EXISTING_CLUSTER_LOCATION:
        problems.append(
            "existing_cluster_name and existing_cluster_location are for cluster_mode=existing; "
            "with cluster_mode=new the module creates the cluster, so leave them empty"
        )
    if problems:
        sys.stderr.write("\n".join(f"  ERROR: {p}" for p in problems) + "\n")
        sys.exit(1)


main()
