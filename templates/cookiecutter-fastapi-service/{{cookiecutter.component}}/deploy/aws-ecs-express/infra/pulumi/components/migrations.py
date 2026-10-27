"""The migration task: a Fargate task definition the CD pipeline runs before every deploy.

A task definition rather than a second service, because a migration is a process that runs to
completion and exits — `cd.yml` runs it with `aws ecs run-task`, waits for it to stop, and fails the
deploy unless it exited 0.

**Ownership works differently here than on the service, on purpose.** A task definition is
immutable: every change is a new *revision* of the family. CD does not edit Pulumi's revision; it
registers its own, derived from the latest ACTIVE revision of the family with only the image
changed, and runs that. So Pulumi's revision is the *shape* — command, roles, secrets, logs — and
there is nothing for `ignore_changes` to protect: the image in Pulumi's revision is the first
deploy's, every deploy replaces it in a revision of its own, and a change to the shape here
registers a new revision that the next deploy derives from. Ignoring changes to the container
definitions would instead freeze the shape at whatever it was on the first apply.

Like the service, it exists only once there is a real image to put in it — `serviceImage`, set by
`just infra-first-deploy`.

**The one secret reference in this stack that is not pinned to a version.** The migration logs in
as the RDS master user, and RDS owns that secret: it generates the password and rotates it, and a
reference pinned to a `VersionId` would break the migration at the first rotation. So the task reads
the secret's current value — `<arn>:username::` and `<arn>:password::`, a JSON key and no version —
when it starts. That is ADR-0016's concern turned the right way round: a task runs once and exits,
so a rotation can only ever affect the next run, never a mix of old and new within one.
"""

from __future__ import annotations

from collections.abc import Mapping, Sequence

import pulumi
import pulumi_aws as aws

from config import StackConfig


def current_json_key(secret_arn: pulumi.Input[str], key: str) -> pulumi.Output[str]:
    """An ECS `valueFrom` for one JSON key of a secret's *current* value: `<arn>:<key>::`.

    For a secret whose owner rotates it — RDS's master secret — and nothing else. Every secret this
    stack creates is pinned with `SecretRef.value_from` instead.
    """
    return pulumi.Output.concat(secret_arn, ":", key, "::")


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
        image: pulumi.Input[str],
        command: Sequence[str],
        execution_role_arn: pulumi.Input[str],
        log_group: pulumi.Input[str],
        env: Mapping[str, pulumi.Input[str]],
        secrets: Mapping[str, pulumi.Input[str]],
        task_role_arn: pulumi.Input[str] | None = None,
        depends_on: Sequence[pulumi.Resource] = (),
        opts: pulumi.ResourceOptions | None = None,
    ) -> None:
        """`secrets` maps an environment variable to its ECS `valueFrom`."""
        super().__init__("service:infra:Migrations", config.name(app, "migrate"), None, opts)

        self.task_definition = aws.ecs.TaskDefinition(
            f"{app}-migrate",
            family=config.name(app, "migrate"),
            requires_compatibilities=["FARGATE"],
            network_mode="awsvpc",
            cpu=CPU,
            memory=MEMORY,
            # Its own execution role, the only one that can read the master secret.
            execution_role_arn=execution_role_arn,
            # None by default: a migration calls no AWS API — it logs in with a password, not a
            # token — so it is given no AWS identity at all.
            task_role_arn=task_role_arn,
            container_definitions=pulumi.Output.json_dumps(
                [
                    {
                        "name": CONTAINER,
                        "image": image,
                        "essential": True,
                        "command": list(command),
                        "environment": [{"name": k, "value": v} for k, v in env.items()],
                        "secrets": [
                            {"name": name, "valueFrom": value_from}
                            for name, value_from in secrets.items()
                        ],
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
