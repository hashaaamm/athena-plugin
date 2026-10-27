"""CloudWatch log groups the stack owns, one per thing that runs.

Declared here and handed to the Express service and the migration task, rather than left to Express
Mode's default. The default group has no retention — it keeps every line forever, and storage is
billed — and it is not deleted with the service, so a destroy leaves it behind. A group this stack
creates has a retention and goes with the stack.
"""

from __future__ import annotations

from collections.abc import Sequence

import pulumi
import pulumi_aws as aws

from config import StackConfig

#: Days kept outside production: enough to debug last week's deploy, short enough to cost nothing.
RETENTION_DAYS = 30

#: Production keeps a quarter, which is what an incident review usually reaches back for.
PRODUCTION_RETENTION_DAYS = 90


class Logs(pulumi.ComponentResource):
    def __init__(
        self,
        config: StackConfig,
        *,
        apps: Sequence[str],
        opts: pulumi.ResourceOptions | None = None,
    ) -> None:
        super().__init__("service:infra:Logs", config.name("logs"), None, opts)

        retention = PRODUCTION_RETENTION_DAYS if config.is_production else RETENTION_DAYS

        #: app -> its log group, `/ecs/<app>-<env>`.
        self.groups: dict[str, aws.cloudwatch.LogGroup] = {
            app: aws.cloudwatch.LogGroup(
                f"{app}-logs",
                name=f"/ecs/{config.name(app)}",
                retention_in_days=retention,
                opts=pulumi.ResourceOptions(parent=self),
            )
            for app in apps
        }

        self.register_outputs({})
