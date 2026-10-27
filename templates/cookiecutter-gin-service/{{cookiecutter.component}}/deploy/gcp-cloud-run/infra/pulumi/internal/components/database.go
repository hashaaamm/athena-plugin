package components

import (
	"strings"

	"github.com/pulumi/pulumi-gcp/sdk/v10/go/gcp/sql"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"{{ cookiecutter.go_module }}/infra/pulumi/internal/stackconfig"
)

const (
	// DBName is the logical database. The service and the migration job both name it.
	DBName = "app"

	// AdminUser is the login the migration job connects as: Cloud SQL's built-in `postgres` user,
	// which owns the schema. The stack never sets or sees its password — `just infra-secrets`
	// does, out of band, and stores it in the db-admin-password container.
	AdminUser = "postgres"

	// IAMAuthenticationFlag turns on IAM database authentication on the instance. Without it a
	// CLOUD_IAM_SERVICE_ACCOUNT user exists and cannot log in.
	IAMAuthenticationFlag = "cloudsql.iam_authentication"

	// serviceAccountSuffix is what Cloud SQL drops from a service account's email to make its IAM
	// database user name: `name@project.iam.gserviceaccount.com` logs in as `name@project.iam`.
	serviceAccountSuffix = ".gserviceaccount.com"
)

// DatabaseArgs is what Database needs beyond the configuration.
type DatabaseArgs struct {
	// AppServiceAccount is the email of the account the service runs as. It becomes the service's
	// database identity: an IAM database user, with no password anywhere.
	AppServiceAccount pulumi.StringInput
}

// Database is Cloud SQL for PostgreSQL.
//
// The cost decision: a shared-core instance with 10 GB of HDD is the floor for managed Postgres on
// GCP, and it is the only standing charge in this stack. Everything else scales to zero. Every
// setting below that has a cheaper-looking alternative is already at its minimum; infra/README.md
// has the table of what each one is and what the floor costs you — shared-core and single-zone are
// both outside the Cloud SQL SLA, which is the price of it.
//
// The security decisions worth understanding:
//
//   - The instance keeps a public IP with ZERO authorised networks and connector enforcement
//     REQUIRED. That reads as a mistake and is not. Every client goes through a Cloud SQL connector
//     — the service through the Go connector in-process, the migration job through Cloud Run's Cloud
//     SQL connection — which authenticates with IAM and does not traverse the public internet; with
//     no authorised networks and connectors required there is no route for anything else, so the
//     address is unreachable rather than merely unguarded. The alternative — private IP — needs a
//     VPC connector or Direct VPC egress, which costs more per month than the database. Ask Athena
//     for the GCP infrastructure standards: a public-IP instance has zero authorised networks and
//     connector enforcement REQUIRED.
//   - Two database identities. The service logs in as its own service account, an IAM database
//     user with no password, holding row privileges only. The migration job logs in as AdminUser,
//     which owns the schema; its password never passes through this program. Pulumi standards
//     MUST-3, and ADR-0023.
type Database struct {
	pulumi.ResourceState

	Instance *sql.DatabaseInstance
	AppUser  *sql.User
	Database *sql.Database

	// ConnectionName is `project:region:instance`: CLOUD_SQL_INSTANCE for the service's connector,
	// and the instance Cloud Run's Cloud SQL volume names for the migration job.
	ConnectionName pulumi.StringOutput

	// AppUserName is the service's database user, `<account>@<project>.iam` — DATABASE_USER for
	// the service, and the role the migration job grants to.
	AppUserName pulumi.StringOutput
}

// NewDatabase creates the instance, the service's IAM database user and the database.
func NewDatabase(ctx *pulumi.Context, cfg stackconfig.StackConfig, args DatabaseArgs, opts ...pulumi.ResourceOption) (*Database, error) {
	c := &Database{}
	if err := ctx.RegisterComponentResource("service:infra:Database", cfg.Name("database"), c, opts...); err != nil {
		return nil, err
	}

	var err error
	c.Instance, err = sql.NewDatabaseInstance(ctx, "postgres", &sql.DatabaseInstanceArgs{
		Project:         pulumi.String(cfg.Project),
		Region:          pulumi.String(cfg.Region),
		Name:            pulumi.String(cfg.Name(cfg.Slug, "pg")),
		DatabaseVersion: pulumi.String("POSTGRES_16"),
		// Guard rail, not paranoia: a `pulumi destroy` that takes the database with it is the one
		// mistake with no undo. Flip this deliberately when you actually mean it.
		DeletionProtection: pulumi.Bool(cfg.IsProduction()),
		Settings: &sql.DatabaseInstanceSettingsArgs{
			Tier:             pulumi.String(cfg.DatabaseTier),
			Edition:          pulumi.String("ENTERPRISE"),
			AvailabilityType: pulumi.String("ZONAL"), // REGIONAL doubles the cost
			DiskType:         pulumi.String("PD_HDD"),
			DiskSize:         pulumi.Int(cfg.DatabaseDiskGB),
			DiskAutoresize:   pulumi.Bool(true),
			IpConfiguration: &sql.DatabaseInstanceSettingsIpConfigurationArgs{
				Ipv4Enabled: pulumi.Bool(true),
				// Nothing may connect directly. See the type's documentation.
				AuthorizedNetworks: sql.DatabaseInstanceSettingsIpConfigurationAuthorizedNetworkArray{},
				SslMode:            pulumi.String("ENCRYPTED_ONLY"),
			},
			// Every connection through a connector, so every connection is authenticated by IAM
			// before Postgres sees it. See the type's documentation.
			ConnectorEnforcement: pulumi.String("REQUIRED"),
			DatabaseFlags: sql.DatabaseInstanceSettingsDatabaseFlagArray{
				&sql.DatabaseInstanceSettingsDatabaseFlagArgs{
					Name:  pulumi.String(IAMAuthenticationFlag),
					Value: pulumi.String("on"),
				},
			},
			BackupConfiguration: &sql.DatabaseInstanceSettingsBackupConfigurationArgs{
				Enabled:                    pulumi.Bool(true),
				StartTime:                  pulumi.String("03:00"),
				PointInTimeRecoveryEnabled: pulumi.Bool(cfg.IsProduction()),
				BackupRetentionSettings: &sql.DatabaseInstanceSettingsBackupConfigurationBackupRetentionSettingsArgs{
					RetainedBackups: pulumi.Int(7),
				},
			},
			MaintenanceWindow: &sql.DatabaseInstanceSettingsMaintenanceWindowArgs{
				Day:         pulumi.Int(7),
				Hour:        pulumi.Int(4),
				UpdateTrack: pulumi.String("stable"),
			},
			InsightsConfig: &sql.DatabaseInstanceSettingsInsightsConfigArgs{
				QueryInsightsEnabled: pulumi.Bool(true),
			},
		},
	}, child(c, opts)...)
	if err != nil {
		return nil, err
	}

	// The service's identity in Postgres. A CLOUD_IAM_SERVICE_ACCOUNT user has no password field at
	// all, so there is nothing secret in state; Cloud SQL accepts the account's own credential. It
	// starts with no privileges: the migration job grants them, after every run of the migrations.
	c.AppUserName = args.AppServiceAccount.ToStringOutput().ApplyT(IAMDatabaseUser).(pulumi.StringOutput)
	c.AppUser, err = sql.NewUser(ctx, "app-user", &sql.UserArgs{
		Project:  pulumi.String(cfg.Project),
		Instance: c.Instance.Name,
		Name:     c.AppUserName,
		Type:     pulumi.String("CLOUD_IAM_SERVICE_ACCOUNT"),
	}, child(c, opts)...)
	if err != nil {
		return nil, err
	}

	// The DependsOn is load-bearing and it is here for the DESTROY, which runs this graph
	// backwards: a dependent is deleted before the thing it depends on, so the database goes first
	// and the IAM user second. Without the edge Pulumi has no ordering between them and deletes both
	// at once. Cloud SQL then drops the user's role while the database still holds the grants the
	// migration job gave it, Postgres refuses with `role ... cannot be dropped because some objects
	// depend on it`, and the destroy fails with the instance and the role still standing. Sharing an
	// instance is not a dependency Pulumi can infer — `Instance:` ties both of these to the
	// instance, not to each other.
	c.Database, err = sql.NewDatabase(ctx, "database", &sql.DatabaseArgs{
		Project:  pulumi.String(cfg.Project),
		Instance: c.Instance.Name,
		Name:     pulumi.String(DBName),
	}, child(c, opts, pulumi.DependsOn([]pulumi.Resource{c.AppUser}))...)
	if err != nil {
		return nil, err
	}

	c.ConnectionName = c.Instance.ConnectionName
	return c, ctx.RegisterResourceOutputs(c, pulumi.Map{
		"connectionName": c.ConnectionName,
		"appUser":        c.AppUserName,
	})
}

// IAMDatabaseUser is the Cloud SQL IAM database user name for a service account's email.
func IAMDatabaseUser(email string) string {
	return strings.TrimSuffix(email, serviceAccountSuffix)
}
