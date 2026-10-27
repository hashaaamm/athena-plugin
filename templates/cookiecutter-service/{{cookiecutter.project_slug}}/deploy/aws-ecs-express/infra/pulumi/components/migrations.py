"""The migration task: a Fargate task definition the CD pipeline runs before every deploy.

A task definition rather than a second service, because a migration is a process that runs to
completion and exits — `cd.yml` runs it with `aws ecs run-task`, waits for it to stop, and fails the
deploy unless it exited 0.

**Ownership works differently here than on the service, on purpose.** A task definition is
immutable: every change is a new *revision* of the family. CD does not edit Pulumi's revision; it
registers its own, derived from the latest ACTIVE revision of the family with only the image
changed, and runs that. So Pulumi's revision is the *shape* — command, roles, secrets, logs — and
there is nothing for `ignore_changes` to protect: the image in Pulumi's revision is always the
bootstrap one, nothing ever runs it, and a change to the shape here registers a new revision that
the next deploy derives from. Ignoring changes to the container definitions would instead freeze the
shape at whatever it was on the first apply.
"""

from __future__ import annotations

from collections.abc import Mapping, Sequence

import pulumi
import pulumi_aws as aws

from components.secrets import SecretRef
from components.service import BOOTSTRAP_IMAGE
from config import StackConfig

#: The smallest Fargate task. A migration is one connection and a handful of DDL statements.
CPU = "256"
MEMORY = "512"

#: The container's name. cd.yml does not depend on it — it edits `containerDefinitions[0]` — but
#: the log stream is `ecs/<container>/<task id>`, and that is how a failed run finds its own log.
CONTAINER = "migrate"


class Migrations(pulumi.ComponentResource):
    def __init__(
        self,
        config: StackConfig,
        *,
        app: str,
        command: Sequence[str],
        execution_role_arn: pulumi.Input[str],
        task_role_arn: pulumi.Input[str],
        log_group: pulumi.Input[str],
        env: Mapping[str, str],
        secret_env: Mapping[str, str],
        secret_refs: Mapping[str, SecretRef],
        depends_on: Sequence[pulumi.Resource] = (),
        opts: pulumi.ResourceOptions | None = None,
    ) -> None:
        super().__init__("service:infra:Migrations", config.name(app, "migrate"), None, opts)

        for key in secret_env.values():
            if key not in secret_refs:
                raise KeyError(f"{app} migrate: no secret named {key!r} was passed to it")

        secrets = [
            {"name": name, "valueFrom": secret_refs[key].value_from()}
            for name, key in secret_env.items()
        ]

        self.task_definition = aws.ecs.TaskDefinition(
            f"{app}-migrate",
            family=config.name(app, "migrate"),
            requires_compatibilities=["FARGATE"],
            network_mode="awsvpc",
            cpu=CPU,
            memory=MEMORY,
            execution_role_arn=execution_role_arn,
            # The application's identity, the same as the service's: a migration is the application
            # acting on its own database, and it should need nothing the service does not have.
            task_role_arn=task_role_arn,
            container_definitions=pulumi.Output.json_dumps(
                [
                    {
                        "name": CONTAINER,
                        "image": BOOTSTRAP_IMAGE,
                        "essential": True,
                        "command": list(command),
                        "environment": [{"name": k, "value": v} for k, v in env.items()],
                        "secrets": secrets,
                        "logConfiguration": {
                            "logDriver": "awslogs",
                            "options": {
                                "awslogs-group": log_group,
                                "awslogs-region": config.region,
                                "awslogs-stream-prefix": "ecs",
                            },
                        },
                    }
                ]
            ),
            # Track the revision this stack registered, not the family's latest. CD registers newer
            # revisions on every deploy; following them would make every refresh report the image
            # CD wrote as drift on a field this stack does not own.
            track_latest=False,
            opts=pulumi.ResourceOptions(parent=self, depends_on=list(depends_on)),
        )

        self.family = self.task_definition.family
        self.register_outputs({"family": self.family})
