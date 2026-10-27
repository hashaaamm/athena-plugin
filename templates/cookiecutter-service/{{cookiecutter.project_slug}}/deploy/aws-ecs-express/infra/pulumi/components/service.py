"""ECS Express Mode: one service, its load balancer and its scaling, as one resource.

Express Mode is ECS with the load balancer, the target group, the HTTPS listener and certificate,
the service security group and the auto-scaling policy provisioned for you — by the infrastructure
role, on the stack's behalf. One Application Load Balancer is shared by up to 25 Express services in
a VPC.

Division of ownership, which is what keeps Pulumi and the CD pipeline from fighting: **this stack
owns the shape** — CPU, memory, scaling, roles, network, secret wiring, logs — and **the pipeline
owns the image**. The image is therefore declared with `ignore_changes`.

`ignore_changes` alone is **not sufficient**, and getting this wrong silently rolls the service back
to the bootstrap image. It suppresses the *diff* between the program and Pulumi's state — but when
CD updates the image out of band, state is never told. Program and state both still say `hello`, so
there is no diff to ignore, and the next update of any unrelated field sends the whole container,
stale image included, and ECS dutifully deploys it.

The missing half is `--refresh`: it pulls the live image into state first, and `ignore_changes` then
preserves *that* rather than the program's constant. Every apply path here passes it, and the
justfile recipes are the enforcement.

`Service` describes *an Express service in this stack*, not an app. Everything that varies — the
port, the health path, the roles, secrets, logs — is an argument with a default, never a branch
inside the component. That is what makes a second service one more instantiation in `stack.py`
rather than a copied file with a different string in it.
"""

from __future__ import annotations

from collections.abc import Mapping, Sequence

import pulumi
import pulumi_aws as aws

from components.secrets import SecretRef
from config import StackConfig

#: Placeholder until the first CD run, and only that. ECR is empty when the stack is first applied —
#: nothing has pushed to it yet — and Express Mode will not create a service without an image. This
#: one is Google's public Cloud Run sample, used here on AWS for its properties and nothing else:
#: it is public with anonymous pull, small, listens on $PORT (default 8080), and answers 200 on
#: every path including /health/live, so the first deployment goes healthy. Verified 2026-10-23.
BOOTSTRAP_IMAGE = "us-docker.pkg.dev/cloudrun/container/hello"

#: The one property path the pipeline owns. Pulumi names it by the schema, where `primaryContainer`
#: is a single object.
IMAGE_PATH = "primaryContainer.image"

#: Average CPU, as a percentage of the task's allocation, that scaling holds the service at.
SCALING_TARGET_CPU = 60


def _https(endpoint: str) -> str:
    """Express Mode reports its endpoint as a host name; everything downstream wants a URL."""
    return endpoint if endpoint.startswith(("https://", "http://")) else f"https://{endpoint}"


class Service(pulumi.ComponentResource):
    def __init__(
        self,
        config: StackConfig,
        *,
        app: str,
        cluster: pulumi.Input[str],
        execution_role_arn: pulumi.Input[str],
        infrastructure_role_arn: pulumi.Input[str],
        subnet_ids: pulumi.Input[Sequence[pulumi.Input[str]]],
        security_group_ids: pulumi.Input[Sequence[pulumi.Input[str]]],
        log_group: pulumi.Input[str],
        port: int,
        health_path: str,
        task_role_arn: pulumi.Input[str] | None = None,
        env: Mapping[str, pulumi.Input[str]] | None = None,
        secret_env: Mapping[str, str] | None = None,
        secret_refs: Mapping[str, SecretRef] | None = None,
        cpu: str | None = None,
        memory: str | None = None,
        min_tasks: int | None = None,
        max_tasks: int | None = None,
        depends_on: Sequence[pulumi.Resource] = (),
        opts: pulumi.ResourceOptions | None = None,
    ) -> None:
        super().__init__("service:infra:Service", config.name(app), None, opts)

        env = env or {}
        secret_env = secret_env or {}
        secret_refs = secret_refs or {}

        # Fail here rather than mid-apply. A missing key surfaces otherwise as a task that cannot
        # resolve a secret, which reads as "the app is broken".
        for key in secret_env.values():
            if key not in secret_refs:
                raise KeyError(f"{app}: no secret named {key!r} was passed to this service")

        self.service = aws.ecs.ExpressGatewayService(
            app,
            service_name=config.name(app),
            cluster=cluster,
            cpu=cpu or config.cpu,
            memory=memory or config.memory,
            execution_role_arn=execution_role_arn,
            infrastructure_role_arn=infrastructure_role_arn,
            task_role_arn=task_role_arn,
            # Liveness, never readiness, for the same reason as on any platform with a health-gated
            # rollout: readiness can legitimately be false on a first deploy — a database not yet
            # migrated — and a check reading it would never let the first deployment go healthy.
            health_check_path=health_path,
            # The load balancer and the tasks share these subnets — public ones, so the load
            # balancer is internet-facing and the tasks can reach ECR without a NAT gateway. Our
            # security group rides alongside the one Express Mode attaches: it is the identity the
            # database trusts. See components/network.py.
            network_configurations=[
                aws.ecs.ExpressGatewayServiceNetworkConfigurationArgs(
                    subnets=subnet_ids,
                    security_groups=security_group_ids,
                )
            ],
            scaling_targets=[
                aws.ecs.ExpressGatewayServiceScalingTargetArgs(
                    min_task_count=min_tasks if min_tasks is not None else config.min_tasks,
                    max_task_count=max_tasks if max_tasks is not None else config.max_tasks,
                    auto_scaling_metric="AVERAGE_CPU",
                    auto_scaling_target_value=SCALING_TARGET_CPU,
                )
            ],
            primary_container=aws.ecs.ExpressGatewayServicePrimaryContainerArgs(
                image=BOOTSTRAP_IMAGE,
                container_port=port,
                environments=[
                    aws.ecs.ExpressGatewayServicePrimaryContainerEnvironmentArgs(name=k, value=v)
                    for k, v in env.items()
                ],
                secrets=[
                    aws.ecs.ExpressGatewayServicePrimaryContainerSecretArgs(
                        name=name, value_from=secret_refs[key].value_from()
                    )
                    for name, key in secret_env.items()
                ],
                aws_logs_configurations=[
                    aws.ecs.ExpressGatewayServicePrimaryContainerAwsLogsConfigurationArgs(
                        log_group=log_group,
                        log_stream_prefix="ecs",
                    )
                ],
            ),
            # Return once ECS has accepted the service, not once it is healthy. The first apply
            # runs the bootstrap image, so waiting proves nothing about the application, and CD
            # waits for stability itself on every real deploy.
            wait_for_steady_state=False,
            opts=pulumi.ResourceOptions(
                parent=self,
                # On the service itself rather than on this component: a component's `depends_on`
                # orders the component, and says nothing a reader can rely on about its children.
                depends_on=list(depends_on),
                # The pipeline owns the image; this stack owns everything around it.
                ignore_changes=[IMAGE_PATH],
            ),
        )

        self.name = self.service.service_name
        self.arn = self.service.service_arn
        #: The public URL Express Mode assigned. Read from the service, because the host name is
        #: generated and nothing can type it in ahead of time.
        self.url = self.service.ingress_paths.apply(
            lambda paths: _https(paths[0].endpoint) if paths else ""
        )
        self.register_outputs({"url": self.url})
