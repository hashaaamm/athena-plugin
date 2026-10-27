"""The ECS cluster every service and task in this stack runs in.

Its own rather than the account's `default` cluster, so a destroy removes it and two stacks in one
account do not share a namespace for their services and tasks — the deploy role's permissions are
scoped to this cluster's ARN, which only means something when the cluster is this stack's.
"""

from __future__ import annotations

import pulumi
import pulumi_aws as aws

from config import StackConfig


class Cluster(pulumi.ComponentResource):
    def __init__(self, config: StackConfig, opts: pulumi.ResourceOptions | None = None) -> None:
        super().__init__("service:infra:Cluster", config.name("cluster"), None, opts)

        self.cluster = aws.ecs.Cluster(
            "cluster",
            name=config.name(config.slug),
            settings=[
                aws.ecs.ClusterSettingArgs(
                    name="containerInsights",
                    # Enhanced Container Insights is per-task metrics in CloudWatch, and CloudWatch
                    # bills custom metrics by the metric. Worth it where an incident can happen to
                    # somebody; off where it cannot.
                    value="enhanced" if config.is_production else "disabled",
                )
            ],
            opts=pulumi.ResourceOptions(parent=self),
        )

        self.name = self.cluster.name
        self.arn = self.cluster.arn
        self.register_outputs({"name": self.name})
