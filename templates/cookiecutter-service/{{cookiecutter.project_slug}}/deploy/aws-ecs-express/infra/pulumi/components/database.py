"""RDS for PostgreSQL.

The cost decision: `db.t4g.micro` with 20 GB of gp3, single-AZ, is the floor for a managed Postgres
on AWS, and it bills whether anybody uses it or not. Every setting below that has a cheaper-looking
alternative is already at its minimum; `infra/README.md` has the table of what each one is and what
the floor costs you — a single-AZ instance has no standby, so a zone outage is an outage.

The security decision: the instance is **not publicly accessible**, lives in subnets with no route
out of the VPC, and its security group admits Postgres from the `app` group and nothing else. The
service reaches it over the VPC's own network. And every connection is TLS: the parameter group
sets `rds.force_ssl`, so a client that does not ask for TLS is refused rather than served in the
clear.
"""

from __future__ import annotations

from collections.abc import Sequence

import pulumi
import pulumi_aws as aws
import pulumi_random as random

from config import StackConfig

#: The logical database and the role that owns it. One name, used in both places and in the DSN.
DB_NAME = "app"

#: RDS's certificate authority for the instance's server certificate. Named rather than left to the
#: account default, which differs by account age and region and changes when AWS rotates CAs.
CA_CERT = "rds-ca-rsa2048-g1"

#: Where storage autoscaling stops. gp3 grows on its own when it fills and never shrinks back, so
#: this is the ceiling on a surprise, not a size anybody chose.
MAX_STORAGE_GB = 100


class Database(pulumi.ComponentResource):
    def __init__(
        self,
        config: StackConfig,
        *,
        subnet_ids: pulumi.Input[Sequence[pulumi.Input[str]]],
        security_group_id: pulumi.Input[str],
        opts: pulumi.ResourceOptions | None = None,
    ) -> None:
        super().__init__("service:infra:Database", config.name("database"), None, opts)
        child = pulumi.ResourceOptions(parent=self)

        self.subnet_group = aws.rds.SubnetGroup(
            "db-subnets",
            name=config.name(config.slug, "db"),
            description=f"{config.slug}: the isolated subnets, which have no route out",
            subnet_ids=subnet_ids,
            opts=child,
        )

        # TLS or nothing. `pending-reboot` rather than `immediate` because it is accepted for a
        # static parameter and a dynamic one alike, and the instance is created with this group, so
        # there is no reboot to wait for: the setting is in force from the first boot.
        self.parameter_group = aws.rds.ParameterGroup(
            "db-parameters",
            name=config.name(config.slug, "pg16"),
            family="postgres16",
            description=f"{config.slug}: refuse connections that are not TLS",
            parameters=[
                aws.rds.ParameterGroupParameterArgs(
                    name="rds.force_ssl", value="1", apply_method="pending-reboot"
                ),
            ],
            opts=child,
        )

        # Generated, never chosen. A password a human typed is a password a human can reuse, and
        # this one only ever travels from Secrets Manager into the task's environment.
        self.password = random.RandomPassword(
            "db-password",
            length=32,
            special=True,
            # Keep it URL-safe: this value ends up inside a DSN.
            override_special="-_.~",
            opts=child,
        )

        self.instance = aws.rds.Instance(
            "postgres",
            identifier=config.db_identifier(),
            engine="postgres",
            # The major version only. RDS picks the current minor, and applies newer ones itself in
            # the maintenance window below.
            engine_version="16",
            auto_minor_version_upgrade=True,
            instance_class=config.database_instance_class,
            allocated_storage=config.database_storage_gb,
            max_allocated_storage=MAX_STORAGE_GB,
            storage_type="gp3",
            storage_encrypted=True,
            multi_az=config.database_multi_az,
            # Nothing outside the VPC can connect, whatever the security group says.
            publicly_accessible=False,
            db_subnet_group_name=self.subnet_group.name,
            parameter_group_name=self.parameter_group.name,
            vpc_security_group_ids=[security_group_id],
            db_name=DB_NAME,
            username=DB_NAME,
            password=self.password.result,
            ca_cert_identifier=CA_CERT,
            backup_retention_period=7,
            backup_window="03:00-03:30",
            maintenance_window="sun:04:00-sun:04:30",
            copy_tags_to_snapshot=True,
            # Guard rail, not paranoia: a `pulumi destroy` that takes the database with it is the
            # one mistake with no undo. Flip this deliberately when you actually mean it.
            deletion_protection=config.is_production,
            # Outside production a destroy should finish, and a final snapshot nobody asked for is
            # a charge that outlives the stack. In production the last state of the data is kept.
            skip_final_snapshot=not config.is_production,
            final_snapshot_identifier=(
                config.name(config.slug, "final") if config.is_production else None
            ),
            # A dev change should land now; a production one waits for the maintenance window
            # rather than restarting the database in the middle of the day.
            apply_immediately=not config.is_production,
            opts=pulumi.ResourceOptions(parent=self, additional_secret_outputs=["password"]),
        )

        #: The DSN the service reads as `DATABASE_URL_OVERRIDE`, in the form the backend's async
        #: SQLAlchemy engine takes: `postgresql+asyncpg://`, and `ssl=require`, which the asyncpg
        #: dialect hands to `asyncpg.connect` — asyncpg has no `sslmode`.
        #:
        #: `require` and not `verify-full`: the RDS certificate authority is not in the image's
        #: trust store, and the path to the instance never leaves the VPC. The connection is
        #: encrypted; what is not checked is that the certificate chains to the RDS CA, which on a
        #: network nothing else can reach is a smaller risk than a bundled CA file that goes stale.
        self.database_url: pulumi.Output[str] = pulumi.Output.secret(
            pulumi.Output.format(
                "postgresql+asyncpg://{0}:{1}@{2}:{3}/{0}?ssl=require",
                DB_NAME,
                self.password.result,
                self.instance.address,
                self.instance.port,
            )
        )

        self.identifier = self.instance.identifier
        self.register_outputs({"identifier": self.identifier})
