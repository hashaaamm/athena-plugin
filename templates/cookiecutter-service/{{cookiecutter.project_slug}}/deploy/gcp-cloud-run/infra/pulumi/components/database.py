"""Cloud SQL for PostgreSQL.

The cost decision: a shared-core instance with 10 GB of HDD is the floor for managed Postgres on
GCP, and it is the only standing charge in this stack. Everything else scales to zero. Every
setting below that has a cheaper-looking alternative is already at its minimum; `infra/README.md`
has the table of what each one is and what the floor costs you — shared-core and single-zone are
both outside the Cloud SQL SLA, which is the price of it.

The security decision worth understanding: the instance keeps a public IP with **zero authorised
networks**. That reads as a mistake and is not. Cloud Run reaches it through the Cloud SQL
connector, which authenticates with IAM over a Unix socket and does not traverse the public
internet; with no authorised networks there is no route for anything else, so the address is
unreachable rather than merely unguarded. The alternative — private IP — needs a VPC connector or
Direct VPC egress, which costs more per month than the database.
"""

from __future__ import annotations

import pulumi
import pulumi_gcp as gcp
import pulumi_random as random

from config import StackConfig

#: The logical database and the role that owns it. One name, used in both places and in the DSN.
DB_NAME = "app"


class Database(pulumi.ComponentResource):
    def __init__(self, config: StackConfig, opts: pulumi.ResourceOptions | None = None) -> None:
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

        # Generated, never chosen. A password a human typed is a password a human can reuse, and
        # this one only ever travels from Secret Manager into the runtime environment.
        self.password = random.RandomPassword(
            "db-password",
            length=32,
            special=True,
            # Keep it URL-safe: this value ends up inside a DSN.
            override_special="-_.~",
            opts=pulumi.ResourceOptions(parent=self),
        )

        self.user = gcp.sql.User(
            "db-user",
            project=config.project,
            instance=self.instance.name,
            name=DB_NAME,
            password=self.password.result,
            opts=pulumi.ResourceOptions(parent=self, additional_secret_outputs=["password"]),
        )

        # `depends_on` is load-bearing and it is here for the *destroy*, which runs this graph
        # backwards: a dependent is deleted before the thing it depends on, so the database goes
        # first and the role second. Without the edge Pulumi has no ordering between them and
        # deletes both at once. Cloud SQL then runs `DROP ROLE "app"` while the database `app` is
        # still there holding tables Alembic created, Postgres refuses with `role "app" cannot be
        # dropped because some objects depend on it`, and the destroy fails with the instance and
        # the role still standing. Sharing an instance and a name is not a dependency Pulumi can
        # infer — `instance=self.instance.name` ties both of these to the *instance*, not to each
        # other.
        self.database = gcp.sql.Database(
            "database",
            project=config.project,
            instance=self.instance.name,
            name=DB_NAME,
            opts=pulumi.ResourceOptions(parent=self, depends_on=[self.user]),
        )

        #: The DSN the service reads as `DATABASE_URL_OVERRIDE`. `?host=/cloudsql/<connection>` is
        #: the Unix socket the Cloud Run connector mounts — there is no hostname to resolve and no
        #: port to open.
        self.database_url = pulumi.Output.all(
            self.password.result, self.instance.connection_name
        ).apply(
            lambda parts: (
                f"postgresql+asyncpg://{DB_NAME}:{parts[0]}@/{DB_NAME}?host=/cloudsql/{parts[1]}"
            )
        )

        self.connection_name = self.instance.connection_name
        self.register_outputs({"connection_name": self.connection_name})
