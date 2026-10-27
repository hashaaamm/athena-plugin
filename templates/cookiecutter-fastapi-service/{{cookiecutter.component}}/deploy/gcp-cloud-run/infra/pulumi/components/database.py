"""Cloud SQL for PostgreSQL.

The cost decision: a shared-core instance with 10 GB of HDD is the floor for managed Postgres on
GCP, and it is the only standing charge in this stack. Everything else scales to zero. Every
setting below that has a cheaper-looking alternative is already at its minimum; `infra/README.md`
has the table of what each one is and what the floor costs you — shared-core and single-zone are
both outside the Cloud SQL SLA, which is the price of it.

The security decision worth understanding: the instance keeps a public IP with **zero authorised
networks** and **connector enforcement on**. That reads as a mistake and is not. Every client comes
in through a Cloud SQL connector, which authenticates with IAM and does not traverse an authorised
network; with none configured and enforcement on, there is no route for anything else, so the
address is unreachable rather than merely unguarded. The alternative — private IP — needs a VPC
connector or Direct VPC egress, which costs more per month than the database.

**No password belongs to the service.** Two identities reach the database, and neither has a
password in this program, its configuration or its state (Pulumi standards MUST-3, ADR-0023):

* The service logs in as an IAM database user — the runtime service account, created below as a
  `CLOUD_IAM_SERVICE_ACCOUNT` user — with a short-lived token the connector fetches. It owns
  nothing; the migration grants it the tables.
* The migration logs in as the built-in `postgres` user, whose password `just infra-secrets`
  generates, sets on the instance and stores in Secret Manager. It owns the schema.
"""

from __future__ import annotations

import pulumi
import pulumi_gcp as gcp

from config import StackConfig

#: The logical database. The service and the migration both connect to it.
DB_NAME = "app"

#: The built-in admin login the migration job connects as. Cloud SQL creates it with the instance;
#: `infra/scripts/secrets.sh` sets its password.
ADMIN_USER = "postgres"

#: What Cloud SQL calls a service account's database user: its email, without this suffix.
SERVICE_ACCOUNT_SUFFIX = ".gserviceaccount.com"


def iam_user_name(service_account_email: str) -> str:
    """The Postgres role Cloud SQL creates for a service account's IAM database user."""
    return service_account_email.removesuffix(SERVICE_ACCOUNT_SUFFIX)


class Database(pulumi.ComponentResource):
    def __init__(
        self,
        config: StackConfig,
        *,
        iam_service_account_email: pulumi.Input[str],
        opts: pulumi.ResourceOptions | None = None,
    ) -> None:
        super().__init__("service:infra:Database", config.name("database"), None, opts)

        self.instance = gcp.sql.DatabaseInstance(
            "postgres",
            project=config.project,
            region=config.region,
            name=config.name(config.slug, "pg"),
            database_version="POSTGRES_16",
            # Guard rail, not paranoia: a `pulumi destroy` that takes the database with it is the
            # one mistake with no undo. Flip this deliberately when you actually mean it.
            deletion_protection=config.is_production,
            settings=gcp.sql.DatabaseInstanceSettingsArgs(
                tier=config.database_tier,
                edition="ENTERPRISE",
                availability_type="ZONAL",  # REGIONAL doubles the cost
                disk_type="PD_HDD",
                disk_size=config.database_disk_gb,
                disk_autoresize=True,
                # GCP infrastructure standards: a public-IP instance has zero authorised networks
                # and connector enforcement REQUIRED. Every client here already uses a connector —
                # the service the Cloud SQL Python connector, the migration job Cloud Run's own
                # Cloud SQL connection — so a direct connection is one nothing legitimate makes.
                connector_enforcement="REQUIRED",
                # What lets an IAM principal log in at all. Without it the service's user exists
                # and every token it presents is refused.
                database_flags=[
                    gcp.sql.DatabaseInstanceSettingsDatabaseFlagArgs(
                        name="cloudsql.iam_authentication", value="on"
                    )
                ],
                ip_configuration=gcp.sql.DatabaseInstanceSettingsIpConfigurationArgs(
                    ipv4_enabled=True,
                    # Nothing may connect directly. See this module's docstring.
                    authorized_networks=[],
                    ssl_mode="ENCRYPTED_ONLY",
                ),
                backup_configuration=gcp.sql.DatabaseInstanceSettingsBackupConfigurationArgs(
                    enabled=True,
                    start_time="03:00",
                    point_in_time_recovery_enabled=config.is_production,
                    backup_retention_settings=(
                        gcp.sql.DatabaseInstanceSettingsBackupConfigurationBackupRetentionSettingsArgs(
                            retained_backups=7,
                        )
                    ),
                ),
                maintenance_window=gcp.sql.DatabaseInstanceSettingsMaintenanceWindowArgs(
                    day=7, hour=4, update_track="stable"
                ),
                insights_config=gcp.sql.DatabaseInstanceSettingsInsightsConfigArgs(
                    query_insights_enabled=True,
                ),
            ),
            opts=pulumi.ResourceOptions(parent=self),
        )

        #: The Postgres role the service connects as: `DATABASE_USER` on the service, and
        #: `DATABASE_APP_ROLE` on the migration job, which grants it the tables.
        self.iam_user_name: pulumi.Output[str] = pulumi.Output.from_input(
            iam_service_account_email
        ).apply(iam_user_name)

        # No password: an IAM user authenticates with a token, so there is nothing here for state
        # to hold.
        self.iam_user = gcp.sql.User(
            "iam-user",
            project=config.project,
            instance=self.instance.name,
            name=self.iam_user_name,
            type="CLOUD_IAM_SERVICE_ACCOUNT",
            opts=pulumi.ResourceOptions(parent=self),
        )

        # `depends_on` is load-bearing and it is here for the *destroy*, which runs this graph
        # backwards: a dependent is deleted before the thing it depends on, so the database goes
        # first and the IAM user second. The migration grants that user privileges on every table
        # in the database, and Postgres refuses to drop a role that still holds privileges —
        # `role ... cannot be dropped because some objects depend on it`. Without the edge Pulumi
        # deletes both at once, Cloud SQL tries the role while the database is still there, and the
        # destroy stops with the instance still running and still billing. Sharing an instance is
        # not a dependency Pulumi can infer: `instance=self.instance.name` ties both of these to
        # the *instance*, not to each other.
        self.database = gcp.sql.Database(
            "database",
            project=config.project,
            instance=self.instance.name,
            name=DB_NAME,
            opts=pulumi.ResourceOptions(parent=self, depends_on=[self.iam_user]),
        )

        #: `project:region:instance` — `CLOUD_SQL_INSTANCE` on the service, and the Cloud SQL
        #: connection the migration job mounts.
        self.connection_name = self.instance.connection_name
        self.instance_name = self.instance.name
        self.register_outputs({"connection_name": self.connection_name})
