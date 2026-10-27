"""Enable the GCP service APIs everything else depends on.

First component in the graph, and everything else depends on it. Without that ordering the first
`pulumi up` fails halfway through with "API not enabled" errors that are individually obvious and
collectively a twenty-minute detour.

**Three APIs cannot be enabled here.** Enabling an API is itself an API call — `serviceusage` —
authorised through `cloudresourcemanager`, and the GCP provider resolves the project's default
region and zone through `compute` before it plans a single resource. All three are needed *before*
this component can run, so this component cannot turn them on. `infra/bootstrap/state-bucket.sh`
does, in plain `gcloud`, which has no such problem.

The symptom when that is missed is a `pulumi preview` that fails with `SERVICE_DISABLED` for
`compute.googleapis.com` while pointing at no resource in particular.
"""

from __future__ import annotations

import pulumi
import pulumi_gcp as gcp

from config import StackConfig

#: Enabled by the bootstrap script, before Pulumi runs at all. Listed here so the full set is
#: visible in one place, and asserted against the bootstrap script by a test.
BOOTSTRAP = (
    "cloudresourcemanager.googleapis.com",  # authorises enabling anything else
    "serviceusage.googleapis.com",  # the API that enables APIs
    "compute.googleapis.com",  # the provider reads default region/zone from it
)

#: Everything the stack itself needs, enabled by this component.
REQUIRED = (
    "run.googleapis.com",
    "artifactregistry.googleapis.com",
    "secretmanager.googleapis.com",
    "iam.googleapis.com",
    "iamcredentials.googleapis.com",
    "sts.googleapis.com",
{%- if cookiecutter.use_postgres == "yes" %}
    "sqladmin.googleapis.com",
{%- endif %}
)


class Apis(pulumi.ComponentResource):
    def __init__(self, config: StackConfig, opts: pulumi.ResourceOptions | None = None) -> None:
        super().__init__("service:infra:Apis", config.name("apis"), None, opts)

        self.services = [
            gcp.projects.Service(
                f"api-{service.split('.')[0]}",
                project=config.project,
                service=service,
                # Disabling an API on destroy would break anything else in the project using it.
                disable_on_destroy=False,
                opts=pulumi.ResourceOptions(parent=self),
            )
            for service in REQUIRED
        ]

        self.register_outputs({})
