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

**No password is in this program, its configuration or its state** (Pulumi standards MUST-3,
ADR-0023). Two identities reach the database:

* The service logs in as `databaseAppRole` with **IAM database authentication**: a token signed
  with the task role's credentials for every new connection, accepted because the role is a member
  of `rds_iam` and the task role holds `rds-db:connect` on it. It owns nothing; the migration grants
  it the tables.
* The migration task logs in as the **master user**, whose password RDS generates, stores in a
  Secrets Manager secret it owns, and rotates (`manage_master_user_password`). The stack sees only
  that secret's ARN. The secret is deleted with the instance.
"""

from __future__ import annotations

from collections.abc import Sequence

import pulumi
import pulumi_aws as aws

from config import DB_MASTER_USER, StackConfig

#: The logical database. The service and the migration both connect to it.
DB_NAME = "app"

#: RDS's certificate authority for the instance's server certificate. Named rather than left to the
#: account default, which differs by account age and region and changes when AWS rotates CAs.
CA_CERT = "rds-ca-rsa2048-g1"

#: Where storage autoscaling stops. gp3 grows on its own when it fills and never shrinks back, so
#: this is the ceiling on a surprise, not a size anybody chose.
MAX_STORAGE_GB = 100


def _master_secret_arn(secrets: Sequence[aws.rds.outputs.InstanceMasterUserSecret]) -> str:
    """The one secret RDS reports for a managed master password. Its absence is a broken apply,
    not a value to default: the migration has no other way to log in."""
    arn = secrets[0].secret_arn if secrets else None
    if not arn:
        raise ValueError("RDS reported no master user secret; is manage_master_user_password on?")
    return arn


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
            username=DB_MASTER_USER,
            # RDS generates the master password, keeps it in a Secrets Manager secret of its own and
            # rotates it; this program never sees the value, only the secret's ARN. `password` and
            # this are mutually exclusive, and the secret goes when the instance goes.
            manage_master_user_password=True,
            # What lets a token stand in for a password, for any role granted `rds_iam`.
            iam_database_authentication_enabled=True,
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
            opts=child,
        )

        #: Where the service and the migration connect: `DATABASE_HOST` and `DATABASE_PORT`. Both
        #: connect with TLS required — `sslmode=require` and not `verify-full`: the RDS certificate
        #: authority is not in the image's trust store, and the path to the instance never leaves
        #: the VPC. The connection is encrypted; what is not checked is that the certificate chains
        #: to the RDS CA, which on a network nothing else can reach is a smaller risk than a bundled
        #: CA file that goes stale.
        self.address = self.instance.address
        self.port = self.instance.port.apply(str)
        #: What `rds-db:connect` names the instance by — not its identifier, which can be reused.
        self.resource_id = self.instance.resource_id
        #: The ARN of the secret RDS keeps the master credentials in, as JSON with `username` and
        #: `password` keys. Only the migration task's execution role may read it.
        self.master_secret_arn: pulumi.Output[str] = self.instance.master_user_secrets.apply(
            _master_secret_arn
        )

        self.identifier = self.instance.identifier
        self.register_outputs({"identifier": self.identifier})
