"""Artifact Registry: one Docker repository for every image this repository builds."""

from __future__ import annotations

import pulumi
import pulumi_gcp as gcp

from config import StackConfig

#: Images kept by the cleanup policy. Every merge pushes an immutable `:sha` tag, so without a
#: policy the repository grows without bound and storage quietly becomes a line on the bill.
KEEP_IMAGES = 10

#: Anything older than this is deleted, whatever the keep count says. Thirty days, in seconds —
#: Artifact Registry takes a duration string, not a date.
DELETE_OLDER_THAN = "2592000s"


class Registry(pulumi.ComponentResource):
    def __init__(self, config: StackConfig, opts: pulumi.ResourceOptions | None = None) -> None:
        super().__init__("service:infra:Registry", config.name("registry"), None, opts)

        self.repository = gcp.artifactregistry.Repository(
            "images",
            project=config.project,
            location=config.region,
            repository_id=config.name(config.slug),
            format="DOCKER",
            description=f"Container images for {config.slug}",
            cleanup_policies=[
                gcp.artifactregistry.RepositoryCleanupPolicyArgs(
                    id="keep-recent",
                    action="KEEP",
                    most_recent_versions=(
                        gcp.artifactregistry.RepositoryCleanupPolicyMostRecentVersionsArgs(
                            keep_count=KEEP_IMAGES,
                        )
                    ),
                ),
                gcp.artifactregistry.RepositoryCleanupPolicyArgs(
                    id="delete-old",
                    action="DELETE",
                    condition=gcp.artifactregistry.RepositoryCleanupPolicyConditionArgs(
                        older_than=DELETE_OLDER_THAN,
                    ),
                ),
            ],
            opts=pulumi.ResourceOptions(parent=self),
        )

        #: What `docker push` targets, and what the CD workflow reads as `IMAGE_REPO`.
        self.url = self.repository.repository_id.apply(
            lambda repo: f"{config.registry_host}/{config.project}/{repo}"
        )

        self.register_outputs({"url": self.url})
