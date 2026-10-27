"""ECR: one repository per image this repository builds.

One per image rather than one for all of them, because an ECR repository *is* an image name: the
backend is `<slug>-<env>:<sha>`, and the deploy role's push permission is granted on exactly the
repositories this stack declares.
"""

from __future__ import annotations

import json

import pulumi
import pulumi_aws as aws

from config import StackConfig

#: Images kept by the lifecycle policy. Every merge pushes an immutable `:sha` tag, so without a
#: policy the repository grows without bound and storage quietly becomes a line on the bill. Thirty
#: is a month of daily merges to roll back to.
KEEP_IMAGES = 30


class Registry(pulumi.ComponentResource):
    def __init__(
        self,
        config: StackConfig,
        *,
        app: str,
        opts: pulumi.ResourceOptions | None = None,
    ) -> None:
        super().__init__("service:infra:Registry", config.name(app, "registry"), None, opts)

        self.repository = aws.ecr.Repository(
            f"{app}-images",
            name=config.name(app),
            # A tag is a commit. Mutable tags let a second push move `:abc123` to different bytes,
            # and then neither a rollback nor an incident timeline can say what ran.
            image_tag_mutability="IMMUTABLE",
            image_scanning_configuration=aws.ecr.RepositoryImageScanningConfigurationArgs(
                scan_on_push=True,
            ),
            encryption_configurations=[
                aws.ecr.RepositoryEncryptionConfigurationArgs(encryption_type="AES256"),
            ],
            # A `dev` destroy has to succeed with images in the repository, or every teardown
            # starts with deleting them by hand. Production keeps them: a destroy that takes the
            # only copy of the running image with it is not one to make easy.
            force_delete=not config.is_production,
            opts=pulumi.ResourceOptions(parent=self),
        )

        aws.ecr.LifecyclePolicy(
            f"{app}-keep-recent",
            repository=self.repository.name,
            policy=json.dumps(
                {
                    "rules": [
                        {
                            "rulePriority": 1,
                            "description": f"keep the last {KEEP_IMAGES} images",
                            "selection": {
                                "tagStatus": "any",
                                "countType": "imageCountMoreThan",
                                "countNumber": KEEP_IMAGES,
                            },
                            "action": {"type": "expire"},
                        }
                    ]
                }
            ),
            opts=pulumi.ResourceOptions(parent=self),
        )

        #: What `docker push` targets, and what the CD workflow reads as `IMAGE_REPO`.
        self.url = self.repository.repository_url
        self.arn = self.repository.arn
        self.register_outputs({"url": self.url})
