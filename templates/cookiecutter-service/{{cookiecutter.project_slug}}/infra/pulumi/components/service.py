"""Cloud Run: a service, and optionally the jobs that maintain it.

Division of ownership, which is what keeps Pulumi and the CD pipeline from fighting: **this stack
owns the shape** — CPU, memory, scaling, service account, secret wiring, the Cloud SQL attachment —
and **the pipeline owns the image tag**. Every container image is therefore declared with
`ignore_changes` on the image field.

`ignore_changes` alone is **not sufficient**, and getting this wrong silently rolls the service back
to the bootstrap image. It suppresses the *diff* between the program and Pulumi's state — but when
CD updates the image out of band, state is never told. Program and state both still say `hello`, so
there is no diff to ignore, and the next update of any unrelated field sends the whole template,
stale image included, and Cloud Run dutifully creates a revision running it.

The missing half is `--refresh`: it pulls the live image into state first, and `ignore_changes` then
preserves *that* rather than the program's constant. Every apply path here passes it, and the
justfile recipes are the enforcement.

`Service` describes *a Cloud Run service in this project*, not an app. Everything that varies — the
port, the probe path, a database, secrets, jobs — is an argument with a default, never a branch
inside the component. That is what makes a second service one more instantiation in `__main__.py`
rather than a copied file with a different string in it.
"""

from __future__ import annotations

from collections.abc import Mapping, Sequence
from dataclasses import dataclass, field

import pulumi
import pulumi_gcp as gcp

from config import StackConfig

#: Placeholder until the first CD run. Cloud Run refuses to create a service with no image, and
#: this one is public, tiny and always available.
BOOTSTRAP_IMAGE = "us-docker.pkg.dev/cloudrun/container/hello"


@dataclass(frozen=True, slots=True)
class JobSpec:
    """One Cloud Run Job alongside a service, and optionally a cron that triggers it."""

    name: str
    command: list[str]
    args: list[str]
    env: Mapping[str, str] = field(default_factory=dict)
    #: env var name -> logical secret name in `secret_ids`
    secret_env: Mapping[str, str] = field(default_factory=dict)


def _secret_env(
    name: str, secret_id: pulumi.Input[str]
) -> gcp.cloudrunv2.ServiceTemplateContainerEnvArgs:
    return gcp.cloudrunv2.ServiceTemplateContainerEnvArgs(
        name=name,
        value_source=gcp.cloudrunv2.ServiceTemplateContainerEnvValueSourceArgs(
            secret_key_ref=gcp.cloudrunv2.ServiceTemplateContainerEnvValueSourceSecretKeyRefArgs(
                secret=secret_id, version="latest"
            )
        ),
    )


def _job_secret_env(
    name: str, secret_id: pulumi.Input[str]
) -> gcp.cloudrunv2.JobTemplateTemplateContainerEnvArgs:
    return gcp.cloudrunv2.JobTemplateTemplateContainerEnvArgs(
        name=name,
        value_source=gcp.cloudrunv2.JobTemplateTemplateContainerEnvValueSourceArgs(
            secret_key_ref=gcp.cloudrunv2.JobTemplateTemplateContainerEnvValueSourceSecretKeyRefArgs(
                secret=secret_id, version="latest"
            )
        ),
    )


class Service(pulumi.ComponentResource):
    def __init__(
        self,
        config: StackConfig,
        *,
        app: str,
        runtime_email: pulumi.Input[str],
        port: int,
        health_path: str,
        env: Mapping[str, pulumi.Input[str]] | None = None,
        secret_env: Mapping[str, str] | None = None,
        secret_ids: Mapping[str, pulumi.Output[str]] | None = None,
        connection_name: pulumi.Input[str] | None = None,
        cpu: str = "1",
        memory: str = "512Mi",
        concurrency: int = 40,
        startup_failure_threshold: int = 20,
        jobs: Sequence[JobSpec] = (),
        public: bool = True,
        opts: pulumi.ResourceOptions | None = None,
    ) -> None:
        super().__init__("service:infra:Service", config.name(app), None, opts)

        env = env or {}
        secret_env = secret_env or {}
        secret_ids = secret_ids or {}

        # Fail here rather than mid-apply. A missing key surfaces otherwise as a Cloud Run revision
        # that cannot resolve a secret, which reads as "the app is broken".
        for key in (*secret_env.values(), *(k for job in jobs for k in job.secret_env.values())):
            if key not in secret_ids:
                raise KeyError(f"{app}: no secret named {key!r} was passed to this service")

        # Cloud SQL is opt-in. A service with no database gets no volume and no mount, rather than
        # an empty one — an unused attachment is a permission the service does not need.
        volumes = (
            [
                gcp.cloudrunv2.ServiceTemplateVolumeArgs(
                    name="cloudsql",
                    cloud_sql_instance=gcp.cloudrunv2.ServiceTemplateVolumeCloudSqlInstanceArgs(
                        instances=[connection_name],
                    ),
                )
            ]
            if connection_name is not None
            else []
        )
        mounts = (
            [
                gcp.cloudrunv2.ServiceTemplateContainerVolumeMountArgs(
                    name="cloudsql", mount_path="/cloudsql"
                )
            ]
            if connection_name is not None
            else []
        )

        self.service = gcp.cloudrunv2.Service(
            app,
            project=config.project,
            location=config.region,
            name=config.name(app),
            ingress="INGRESS_TRAFFIC_ALL",
            deletion_protection=False,
            template=gcp.cloudrunv2.ServiceTemplateArgs(
                service_account=runtime_email,
                max_instance_request_concurrency=concurrency,
                scaling=gcp.cloudrunv2.ServiceTemplateScalingArgs(
                    min_instance_count=config.min_instances,
                    max_instance_count=config.max_instances,
                ),
                volumes=volumes,
                containers=[
                    gcp.cloudrunv2.ServiceTemplateContainerArgs(
                        image=BOOTSTRAP_IMAGE,
                        resources=gcp.cloudrunv2.ServiceTemplateContainerResourcesArgs(
                            limits={"cpu": cpu, "memory": memory},
                            cpu_idle=True,  # bill for CPU only while a request is in flight
                            # Without this, `cpu_idle` throttles the container *during* startup
                            # too, and a cold import graph does not finish inside the probe budget.
                            # It costs nothing extra: the boost applies only while starting.
                            startup_cpu_boost=True,
                        ),
                        ports=gcp.cloudrunv2.ServiceTemplateContainerPortsArgs(container_port=port),
                        envs=[
                            *[
                                gcp.cloudrunv2.ServiceTemplateContainerEnvArgs(name=k, value=v)
                                for k, v in env.items()
                            ],
                            *[
                                _secret_env(name, secret_ids[key])
                                for name, key in secret_env.items()
                            ],
                        ],
                        volume_mounts=mounts,
                        # A probe that is marginally too tight fails deploys intermittently, which
                        # reads as "the app is broken" rather than "the budget is wrong". Liveness
                        # here, never readiness: readiness can legitimately be false on a first
                        # deploy, and a startup probe reading it would never let the revision up.
                        startup_probe=gcp.cloudrunv2.ServiceTemplateContainerStartupProbeArgs(
                            initial_delay_seconds=5,
                            period_seconds=5,
                            failure_threshold=startup_failure_threshold,
                            http_get=gcp.cloudrunv2.ServiceTemplateContainerStartupProbeHttpGetArgs(
                                path=health_path, port=port
                            ),
                        ),
                    )
                ],
            ),
            opts=pulumi.ResourceOptions(
                parent=self,
                # The pipeline owns the image; this stack owns everything around it.
                ignore_changes=["template.containers[0].image", "client", "clientVersion"],
            ),
        )

        # Public at the network edge, because a service nothing can reach is not yet a service.
        # Turn it off with `public=False` once the API is behind an identity-aware proxy or is
        # called only by other Google-authenticated workloads; the smoke test in CD then needs an
        # identity token rather than a plain curl.
        if public:
            gcp.cloudrunv2.ServiceIamMember(
                f"{app}-public",
                project=config.project,
                location=config.region,
                name=self.service.name,
                role="roles/run.invoker",
                member="allUsers",
                opts=pulumi.ResourceOptions(parent=self),
            )

        self.jobs = {
            spec.name: self._job(
                config,
                app=app,
                spec=spec,
                runtime_email=runtime_email,
                connection_name=connection_name,
                secret_ids=secret_ids,
            )
            for spec in jobs
        }

        self.url = self.service.uri
        self.register_outputs({"url": self.url})

    def _job(
        self,
        config: StackConfig,
        *,
        app: str,
        spec: JobSpec,
        runtime_email: pulumi.Input[str],
        connection_name: pulumi.Input[str] | None,
        secret_ids: Mapping[str, pulumi.Output[str]],
    ) -> gcp.cloudrunv2.Job:
        volumes = (
            [
                gcp.cloudrunv2.JobTemplateTemplateVolumeArgs(
                    name="cloudsql",
                    cloud_sql_instance=(
                        gcp.cloudrunv2.JobTemplateTemplateVolumeCloudSqlInstanceArgs(
                            instances=[connection_name],
                        )
                    ),
                )
            ]
            if connection_name is not None
            else []
        )
        mounts = (
            [
                gcp.cloudrunv2.JobTemplateTemplateContainerVolumeMountArgs(
                    name="cloudsql", mount_path="/cloudsql"
                )
            ]
            if connection_name is not None
            else []
        )

        return gcp.cloudrunv2.Job(
            f"{app}-{spec.name}",
            project=config.project,
            location=config.region,
            name=config.name(app, spec.name),
            deletion_protection=False,
            template=gcp.cloudrunv2.JobTemplateArgs(
                # One attempt. A failed migration needs a human reading the log, not a retry that
                # fails identically and doubles the noise.
                task_count=1,
                template=gcp.cloudrunv2.JobTemplateTemplateArgs(
                    service_account=runtime_email,
                    max_retries=0,
                    timeout="900s",
                    volumes=volumes,
                    containers=[
                        gcp.cloudrunv2.JobTemplateTemplateContainerArgs(
                            image=BOOTSTRAP_IMAGE,
                            commands=spec.command,
                            args=spec.args,
                            envs=[
                                *[
                                    gcp.cloudrunv2.JobTemplateTemplateContainerEnvArgs(
                                        name=k, value=v
                                    )
                                    for k, v in spec.env.items()
                                ],
                                *[
                                    _job_secret_env(name, secret_ids[key])
                                    for name, key in spec.secret_env.items()
                                ],
                            ],
                            volume_mounts=mounts,
                        )
                    ],
                ),
            ),
            opts=pulumi.ResourceOptions(
                parent=self,
                ignore_changes=[
                    "template.template.containers[0].image",
                    "client",
                    "clientVersion",
                ],
            ),
        )
